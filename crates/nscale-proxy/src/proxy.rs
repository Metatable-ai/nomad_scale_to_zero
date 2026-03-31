use axum::body::Body;
use axum::http::{Request, Response, StatusCode};
use reqwest::Client;
use thiserror::Error;
use tracing::{debug, instrument};

use nscale_core::job::Endpoint;

#[derive(Debug, Error)]
pub enum ForwardRequestError {
    #[error("backend transport failure: {source}")]
    Transport {
        #[source]
        source: reqwest::Error,
    },
    #[error("failed to build response: {source}")]
    ResponseBuild {
        #[source]
        source: axum::http::Error,
    },
}

impl ForwardRequestError {
    pub fn status_code(&self) -> StatusCode {
        match self {
            Self::Transport { .. } => StatusCode::BAD_GATEWAY,
            Self::ResponseBuild { .. } => StatusCode::INTERNAL_SERVER_ERROR,
        }
    }

    pub fn is_retryable_transport(&self) -> bool {
        match self {
            Self::Transport { source } => source.is_connect() || source.is_timeout(),
            Self::ResponseBuild { .. } => false,
        }
    }

    pub fn is_connect(&self) -> bool {
        match self {
            Self::Transport { source } => source.is_connect(),
            Self::ResponseBuild { .. } => false,
        }
    }

    pub fn is_timeout(&self) -> bool {
        match self {
            Self::Transport { source } => source.is_timeout(),
            Self::ResponseBuild { .. } => false,
        }
    }

    pub fn response_status(&self) -> Option<reqwest::StatusCode> {
        match self {
            Self::Transport { source } => source.status(),
            Self::ResponseBuild { .. } => None,
        }
    }
}

/// Reverse-proxy a request to the given backend endpoint.
///
/// Copies method, path + query, headers (except `Host`), and body
/// from the inbound request and streams the backend response back.
#[instrument(skip(client, req), fields(backend = %backend))]
pub async fn forward_request(
    client: &Client,
    backend: &Endpoint,
    mut req: Request<Body>,
) -> Result<Response<Body>, ForwardRequestError> {
    let path_and_query = req
        .uri()
        .path_and_query()
        .map(|pq| pq.as_str())
        .unwrap_or("/");

    let url = format!("{}{}", backend.base_url("http"), path_and_query);
    debug!(url = %url, method = %req.method(), "forwarding request");

    // Build the outbound request
    let mut builder = client.request(req.method().clone(), &url);

    // Copy headers, skipping hop-by-hop and Host
    for (name, value) in req.headers() {
        let n = name.as_str();
        if matches!(
            n,
            "host"
                | "connection"
                | "keep-alive"
                | "transfer-encoding"
                | "te"
                | "trailer"
                | "upgrade"
                | "proxy-authorization"
                | "proxy-connection"
        ) {
            continue;
        }
        builder = builder.header(name.clone(), value.clone());
    }

    // Forward the body
    let body = std::mem::replace(req.body_mut(), Body::empty());
    let body_stream = body.into_data_stream();
    builder = builder.body(reqwest::Body::wrap_stream(body_stream));

    let response = builder
        .send()
        .await
        .map_err(|source| ForwardRequestError::Transport { source })?;

    // Convert reqwest response back to axum response
    let status =
        StatusCode::from_u16(response.status().as_u16()).unwrap_or(StatusCode::BAD_GATEWAY);
    let mut resp_builder = Response::builder().status(status);

    for (name, value) in response.headers() {
        resp_builder = resp_builder.header(name.clone(), value.clone());
    }

    let body_bytes = response.bytes_stream();
    let body = Body::from_stream(body_bytes);

    resp_builder
        .body(body)
        .map_err(|source| ForwardRequestError::ResponseBuild { source })
}

#[cfg(test)]
mod tests {
    use std::net::TcpListener;

    use super::*;
    use axum::body::to_bytes;
    use wiremock::matchers::{body_string, header, method, path};
    use wiremock::{Mock, MockServer, ResponseTemplate};

    async fn assert_forward_request_preserves_method_and_body(
        request_method: &str,
        request_path: &str,
        request_body: &str,
    ) {
        let mock_server = MockServer::start().await;

        Mock::given(method(request_method))
            .and(path(request_path))
            .and(header("x-test-header", "nscale"))
            .and(body_string(request_body))
            .respond_with(ResponseTemplate::new(202).set_body_string("forwarded"))
            .expect(1)
            .mount(&mock_server)
            .await;

        let client = Client::builder().build().expect("client should build");
        let request = Request::builder()
            .method(request_method)
            .uri(request_path)
            .header("host", "echo-s2z.example")
            .header("x-test-header", "nscale")
            .body(Body::from(request_body.to_owned()))
            .expect("request should build");

        let backend = Endpoint::new(
            mock_server.address().ip().to_string(),
            mock_server.address().port(),
        );

        let response = forward_request(&client, &backend, request)
            .await
            .expect("forward request should succeed");

        assert_eq!(response.status(), StatusCode::ACCEPTED);
        let body = to_bytes(response.into_body(), usize::MAX)
            .await
            .expect("response body should be readable");
        assert_eq!(body, "forwarded");
    }

    #[tokio::test]
    async fn forward_request_marks_connect_failures_retryable() {
        let listener = TcpListener::bind("127.0.0.1:0").expect("listener should bind");
        let port = listener
            .local_addr()
            .expect("listener should have an address")
            .port();
        drop(listener);

        let client = Client::builder()
            .connect_timeout(std::time::Duration::from_millis(100))
            .build()
            .expect("client should build");
        let request = Request::builder()
            .method("GET")
            .uri("/")
            .body(Body::empty())
            .expect("request should build");

        let err = forward_request(&client, &Endpoint::new("127.0.0.1", port), request)
            .await
            .expect_err("closed port should fail");

        assert!(err.is_retryable_transport());
        assert!(err.is_connect() || err.is_timeout());
        assert_eq!(err.status_code(), StatusCode::BAD_GATEWAY);
        assert_eq!(err.response_status(), None);
    }

    #[tokio::test]
    async fn forward_request_preserves_post_method_and_body() {
        assert_forward_request_preserves_method_and_body(
            "POST",
            "/submit",
            r#"{"message":"hello from post"}"#,
        )
        .await;
    }

    #[tokio::test]
    async fn forward_request_preserves_put_method_and_body() {
        assert_forward_request_preserves_method_and_body(
            "PUT",
            "/resource/42",
            r#"{"message":"hello from put"}"#,
        )
        .await;
    }
}
