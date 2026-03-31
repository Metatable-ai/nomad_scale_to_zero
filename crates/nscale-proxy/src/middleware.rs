use std::sync::Arc;
use std::task::{Context, Poll};

use axum::body::Body;
use axum::http::{Request, header};
use tower::{Layer, Service};
use tracing::{debug, warn};

use nscale_core::job::JobId;
use nscale_core::traits::ActivityStore;

// ──────────────────────────────────
// Activity recording layer
// ──────────────────────────────────

/// Tower layer that wraps services to record activity after each request.
#[derive(Clone)]
pub struct ActivityLayer {
    store: Arc<dyn ActivityStore>,
}

impl ActivityLayer {
    pub fn new(store: Arc<dyn ActivityStore>) -> Self {
        Self { store }
    }
}

impl<S> Layer<S> for ActivityLayer {
    type Service = ActivityService<S>;

    fn layer(&self, inner: S) -> Self::Service {
        ActivityService {
            inner,
            store: self.store.clone(),
        }
    }
}

/// Tower service wrapper that records activity for each inbound request.
#[derive(Clone)]
pub struct ActivityService<S> {
    inner: S,
    store: Arc<dyn ActivityStore>,
}

impl<S> Service<Request<Body>> for ActivityService<S>
where
    S: Service<Request<Body>> + Clone + Send + 'static,
    S::Future: Send + 'static,
{
    type Response = S::Response;
    type Error = S::Error;
    type Future = std::pin::Pin<
        Box<dyn std::future::Future<Output = Result<Self::Response, Self::Error>> + Send>,
    >;

    fn poll_ready(&mut self, cx: &mut Context<'_>) -> Poll<Result<(), Self::Error>> {
        self.inner.poll_ready(cx)
    }

    fn call(&mut self, req: Request<Body>) -> Self::Future {
        // Extract job id from Host header before passing request to inner service
        let job_id = req
            .headers()
            .get(header::HOST)
            .and_then(|v| v.to_str().ok())
            .map(|host| {
                host.split('.')
                    .next()
                    .unwrap_or(host)
                    .split(':')
                    .next()
                    .unwrap_or(host)
                    .to_string()
            });

        let store = self.store.clone();
        let future = self.inner.call(req);

        Box::pin(async move {
            // Record activity at request START so the idle timer is pushed
            // forward immediately — this protects against scale-down during
            // long-running requests.
            if let Some(ref id) = job_id {
                let job = JobId(id.clone());
                debug!(job_id = %id, source = "middleware-start", "recording activity");
                if let Err(e) = store.record_activity(&job).await {
                    warn!(job_id = %id, error = %e, "failed to record start activity");
                }
            }

            let response = future.await?;

            // Also record activity at request END (fire-and-forget)
            if let Some(id) = job_id {
                let store = store.clone();
                tokio::spawn(async move {
                    let job = JobId(id.clone());
                    debug!(job_id = %id, source = "middleware-end", "recording activity");
                    if let Err(e) = store.record_activity(&job).await {
                        warn!(job_id = %id, error = %e, "failed to record activity");
                    }
                });
            }

            Ok(response)
        })
    }
}
