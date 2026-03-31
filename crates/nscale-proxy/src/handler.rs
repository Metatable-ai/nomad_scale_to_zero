use std::sync::Arc;
use std::time::Duration;

use axum::{
    body::Body,
    extract::State,
    http::{Method, Request, StatusCode, header},
    response::{IntoResponse, Response},
};
use tracing::{error, info, instrument, warn};

use nscale_core::inflight::InFlightTracker;
use nscale_core::job::JobId;
use nscale_core::traits::ActivityStore;
use nscale_store::registry::JobRegistry;
use nscale_waker::coordinator::{EndpointRefresh, WakeCoordinator};

use crate::proxy::forward_request;

const TRANSIENT_RETRY_BACKOFF_MS: [u64; 2] = [100, 250];

struct CancelOnDrop(tokio_util::sync::CancellationToken);

impl Drop for CancelOnDrop {
    fn drop(&mut self) {
        self.0.cancel();
    }
}

/// Shared application state for the proxy handler.
#[derive(Clone)]
pub struct AppState {
    pub coordinator: Arc<WakeCoordinator>,
    pub registry: Arc<JobRegistry>,
    pub http_client: reqwest::Client,
    pub in_flight: InFlightTracker,
    pub activity_store: Arc<dyn ActivityStore>,
    /// Interval for refreshing activity during long-running proxied requests.
    pub heartbeat_interval: Duration,
}

fn replayable_method(method: &Method) -> Option<Method> {
    match *method {
        Method::GET => Some(Method::GET),
        Method::HEAD => Some(Method::HEAD),
        _ => None,
    }
}

fn build_retry_request(method: &Method, uri: &str) -> Request<Body> {
    Request::builder()
        .method(method.clone())
        .uri(uri)
        .body(Body::empty())
        .expect("retry request should reuse a valid method and URI")
}

/// Main request handler.
///
/// 1. Extract service name from the `Host` header (first label before `.`).
/// 2. Look up the `JobRegistration` in the Redis-backed registry.
/// 3. Ensure the backing job is running (wake if dormant).
/// 4. Forward the request to the healthy backend.
#[instrument(skip_all, fields(host))]
pub async fn proxy_handler(State(state): State<AppState>, req: Request<Body>) -> Response {
    // --- 1. Extract service identifier from Host header ---
    let host_value = match req.headers().get(header::HOST) {
        Some(v) => v.to_str().unwrap_or_default().to_string(),
        None => {
            warn!("request missing Host header");
            return (StatusCode::BAD_REQUEST, "missing Host header").into_response();
        }
    };

    // Use the first label of the host (before the first dot or colon) as the job id.
    let service_key = host_value
        .split('.')
        .next()
        .unwrap_or(&host_value)
        .split(':')
        .next()
        .unwrap_or(&host_value)
        .to_string();

    tracing::Span::current().record("host", service_key.as_str());

    // --- 2. Look up job registration ---
    let job_id = JobId(service_key.clone());
    let registration = match state.registry.get(&job_id).await {
        Ok(Some(reg)) => reg,
        Ok(None) => {
            warn!(job_id = %job_id, "job not found in registry");
            return (StatusCode::NOT_FOUND, "service not registered").into_response();
        }
        Err(e) => {
            error!(error = %e, "registry lookup failed");
            return (StatusCode::INTERNAL_SERVER_ERROR, "registry error").into_response();
        }
    };

    // --- 3. Ensure running (wake if dormant) ---
    let endpoint = match state.coordinator.ensure_running(&registration).await {
        Ok(ep) => ep,
        Err(e) => {
            error!(job_id = %job_id, error = %e, "wake failed");
            return (StatusCode::SERVICE_UNAVAILABLE, format!("wake error: {e}")).into_response();
        }
    };

    info!(
        job_id = %job_id,
        endpoint = %endpoint,
        "routing request to backend"
    );

    let replay_method = replayable_method(req.method());
    let retry_uri = req
        .uri()
        .path_and_query()
        .map(|pq| pq.as_str().to_string())
        .unwrap_or_else(|| "/".to_string());

    // --- Track in-flight request so the scale-down controller skips this job ---
    let _in_flight_guard = state.in_flight.track(&job_id.0);

    // Spawn a heartbeat that refreshes activity while the proxy request is in-flight.
    // This prevents the scale-down controller from treating the job as idle during
    // long-running requests.
    let heartbeat_store = state.activity_store.clone();
    let heartbeat_job = job_id.clone();
    let heartbeat_interval = state.heartbeat_interval;
    let heartbeat_cancel = tokio_util::sync::CancellationToken::new();
    let heartbeat_cancel_clone = heartbeat_cancel.clone();
    let _heartbeat_cancel_guard = CancelOnDrop(heartbeat_cancel.clone());

    tokio::spawn(async move {
        let mut ticker = tokio::time::interval(heartbeat_interval);
        ticker.tick().await; // first tick is immediate, skip it
        loop {
            tokio::select! {
                _ = heartbeat_cancel_clone.cancelled() => break,
                _ = ticker.tick() => {
                    if let Err(e) = heartbeat_store.record_activity(&heartbeat_job).await {
                        warn!(job_id = %heartbeat_job, error = %e, "activity heartbeat failed");
                    }
                }
            }
        }
    });

    // --- 4. Forward request to backend; retry transient transport failures ---
    let result = match forward_request(&state.http_client, &endpoint, req).await {
        Ok(resp) => resp,
        Err(err) if !err.is_retryable_transport() => {
            error!(
                job_id = %job_id,
                endpoint = %endpoint,
                error = %err,
                status = ?err.response_status(),
                "backend request failed"
            );
            heartbeat_cancel.cancel();
            return err.status_code().into_response();
        }
        Err(mut err) => {
            let Some(retry_method) = replay_method else {
                error!(
                    job_id = %job_id,
                    endpoint = %endpoint,
                    error = %err,
                    is_connect = err.is_connect(),
                    is_timeout = err.is_timeout(),
                    status = ?err.response_status(),
                    "backend request failed and request is not safe to replay"
                );
                heartbeat_cancel.cancel();
                return err.status_code().into_response();
            };

            for (attempt, delay_ms) in TRANSIENT_RETRY_BACKOFF_MS.iter().enumerate() {
                tokio::time::sleep(Duration::from_millis(*delay_ms)).await;

                let retry_req = build_retry_request(&retry_method, retry_uri.as_str());
                match forward_request(&state.http_client, &endpoint, retry_req).await {
                    Ok(resp) => {
                        info!(
                            job_id = %job_id,
                            endpoint = %endpoint,
                            attempts = attempt + 1,
                            "retry: original backend recovered"
                        );
                        heartbeat_cancel.cancel();
                        return resp;
                    }
                    Err(retry_err) if retry_err.is_retryable_transport() => {
                        err = retry_err;
                    }
                    Err(retry_err) => {
                        error!(
                            job_id = %job_id,
                            endpoint = %endpoint,
                            error = %retry_err,
                            status = ?retry_err.response_status(),
                            "backend request failed after retry"
                        );
                        heartbeat_cancel.cancel();
                        return retry_err.status_code().into_response();
                    }
                }
            }

            match state
                .coordinator
                .refresh_endpoint(&registration, &endpoint)
                .await
            {
                Ok(EndpointRefresh::Confirmed(_)) => {
                    error!(
                        job_id = %job_id,
                        endpoint = %endpoint,
                        error = %err,
                        is_connect = err.is_connect(),
                        is_timeout = err.is_timeout(),
                        status = ?err.response_status(),
                        "backend request failed after transient retries; cached endpoint still healthy"
                    );
                    heartbeat_cancel.cancel();
                    return err.status_code().into_response();
                }
                Ok(EndpointRefresh::Updated(refreshed_endpoint)) => {
                    warn!(
                        job_id = %job_id,
                        old_endpoint = %endpoint,
                        endpoint = %refreshed_endpoint,
                        "healthy endpoint changed after transient transport failures, retrying refreshed endpoint"
                    );

                    let retry_req = build_retry_request(&retry_method, retry_uri.as_str());
                    match forward_request(&state.http_client, &refreshed_endpoint, retry_req).await
                    {
                        Ok(resp) => resp,
                        Err(refresh_err) => {
                            error!(
                                job_id = %job_id,
                                endpoint = %refreshed_endpoint,
                                error = %refresh_err,
                                is_connect = refresh_err.is_connect(),
                                is_timeout = refresh_err.is_timeout(),
                                status = ?refresh_err.response_status(),
                                "backend request failed after endpoint refresh"
                            );
                            refresh_err.status_code().into_response()
                        }
                    }
                }
                Ok(EndpointRefresh::Missing) => {
                    warn!(
                        job_id = %job_id,
                        endpoint = %endpoint,
                        "backend no longer has a running endpoint, re-waking"
                    );

                    let endpoint = match state.coordinator.ensure_running(&registration).await {
                        Ok(ep) => ep,
                        Err(e) => {
                            error!(job_id = %job_id, error = %e, "retry wake failed");
                            heartbeat_cancel.cancel();
                            return (
                                StatusCode::SERVICE_UNAVAILABLE,
                                format!("retry wake error: {e}"),
                            )
                                .into_response();
                        }
                    };

                    info!(
                        job_id = %job_id,
                        endpoint = %endpoint,
                        "retry: routing request to re-woken backend"
                    );

                    let retry_req = build_retry_request(&retry_method, retry_uri.as_str());
                    match forward_request(&state.http_client, &endpoint, retry_req).await {
                        Ok(resp) => resp,
                        Err(rewake_err) => {
                            error!(
                                job_id = %job_id,
                                endpoint = %endpoint,
                                error = %rewake_err,
                                is_connect = rewake_err.is_connect(),
                                is_timeout = rewake_err.is_timeout(),
                                status = ?rewake_err.response_status(),
                                "backend request failed after re-wake"
                            );
                            rewake_err.status_code().into_response()
                        }
                    }
                }
                Err(e) => {
                    error!(
                        job_id = %job_id,
                        endpoint = %endpoint,
                        error = %e,
                        "failed to refresh backend endpoint after transient transport failures"
                    );
                    heartbeat_cancel.cancel();
                    return StatusCode::BAD_GATEWAY.into_response();
                }
            }
        }
    };

    // Stop the heartbeat — request processing is done.
    heartbeat_cancel.cancel();

    result
}

#[cfg(test)]
mod tests {
    use axum::http::Method;

    use super::{CancelOnDrop, replayable_method};

    #[tokio::test]
    async fn cancel_on_drop_cancels_token() {
        let token = tokio_util::sync::CancellationToken::new();

        {
            let _guard = CancelOnDrop(token.clone());
        }

        tokio::time::timeout(std::time::Duration::from_millis(50), token.cancelled())
            .await
            .expect("token should be canceled when guard drops");
    }

    #[test]
    fn replayable_method_only_allows_safe_retries() {
        assert_eq!(replayable_method(&Method::GET), Some(Method::GET));
        assert_eq!(replayable_method(&Method::HEAD), Some(Method::HEAD));
        assert_eq!(replayable_method(&Method::POST), None);
        assert_eq!(replayable_method(&Method::PUT), None);
    }
}
