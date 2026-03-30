use std::sync::Arc;

use tokio::sync::mpsc;
use tracing::{debug, info, warn};

use nscale_core::job::JobId;
use nscale_core::traits::ActivityStore;
use nscale_nomad::events::NomadEvent;
use nscale_store::registry::JobRegistry;
use nscale_waker::coordinator::WakeCoordinator;

/// Processes Nomad event stream to reactively update job states.
///
/// Replaces the polling-based approach by subscribing to `Allocation` events
/// from Nomad's `/v1/event/stream`. When an allocation transitions to running,
/// activity is recorded. When all allocations stop, the coordinator is notified
/// the job is dormant.
pub struct EventProcessor {
    coordinator: Arc<WakeCoordinator>,
    store: Arc<dyn ActivityStore>,
    registry: Arc<JobRegistry>,
}

impl EventProcessor {
    pub fn new(
        coordinator: Arc<WakeCoordinator>,
        store: Arc<dyn ActivityStore>,
        registry: Arc<JobRegistry>,
    ) -> Self {
        Self {
            coordinator,
            store,
            registry,
        }
    }

    /// Consume events from the Nomad event stream and update internal state.
    pub async fn run(self, mut rx: mpsc::Receiver<NomadEvent>) {
        info!("event processor started");

        while let Some(event) = rx.recv().await {
            if let Err(e) = self.handle_event(&event).await {
                warn!(
                    error = %e,
                    topic = %event.topic,
                    event_type = %event.event_type,
                    "failed to handle nomad event"
                );
            }
        }

        info!("event processor stopped (channel closed)");
    }

    async fn handle_event(&self, event: &NomadEvent) -> nscale_core::error::Result<()> {
        match event.topic.as_str() {
            "Allocation" => self.handle_allocation_event(event).await,
            _ => {
                debug!(topic = %event.topic, "ignoring unhandled event topic");
                Ok(())
            }
        }
    }

    async fn handle_allocation_event(&self, event: &NomadEvent) -> nscale_core::error::Result<()> {
        // Extract allocation fields from the Nomad event payload.
        // The payload structure is: { "Allocation": { "JobID": "...", "ClientStatus": "...", ... } }
        let alloc = match event.payload.get("Allocation") {
            Some(a) => a,
            None => {
                debug!("allocation event missing Allocation key");
                return Ok(());
            }
        };

        let job_id_str = match alloc.get("JobID").and_then(|v| v.as_str()) {
            Some(id) => id,
            None => return Ok(()),
        };

        let client_status = alloc
            .get("ClientStatus")
            .and_then(|v| v.as_str())
            .unwrap_or("");
        let desired_status = alloc
            .get("DesiredStatus")
            .and_then(|v| v.as_str())
            .unwrap_or("");

        let job_id = JobId(job_id_str.to_string());

        // Only process events for registered jobs
        let registration = match self.registry.get(&job_id).await? {
            Some(reg) => reg,
            None => return Ok(()),
        };

        debug!(
            job_id = %job_id,
            client_status,
            desired_status,
            event_type = %event.event_type,
            "processing allocation event"
        );

        match (client_status, desired_status) {
            ("running", "run") => {
                // Allocation is running — seed activity so idle detection starts
                // from this moment.
                debug!(job_id = %job_id, "allocation running, recording activity");
                self.store.record_activity(&job_id).await?;
            }
            ("complete" | "failed" | "lost", _) | (_, "stop") => {
                // Allocation stopped — check if *all* allocations for this job
                // are gone. If so, mark the job as dormant.
                debug!(
                    job_id = %job_id,
                    client_status,
                    desired_status,
                    "allocation stopped"
                );
                // We only mark dormant; the scale-down controller still handles
                // the actual scale-down decision. This just updates the coordinator
                // cache so the next proxy request triggers a wake.
                self.coordinator.mark_dormant(&job_id);
                let _ = self.store.remove_activity(&job_id).await;
                info!(
                    job_id = %job_id,
                    service_name = %registration.service_name,
                    "job marked dormant via event stream"
                );
            }
            _ => {
                debug!(
                    job_id = %job_id,
                    client_status,
                    desired_status,
                    "ignoring allocation state"
                );
            }
        }

        Ok(())
    }
}
