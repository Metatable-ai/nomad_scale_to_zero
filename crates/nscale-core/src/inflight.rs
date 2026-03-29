use std::collections::HashMap;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, RwLock};

/// Thread-safe in-flight request tracker per job.
///
/// Counts currently active proxy requests for each job. The scale-down
/// controller checks this before scaling to avoid killing jobs with
/// in-flight work.
#[derive(Clone, Default)]
pub struct InFlightTracker {
    counts: Arc<RwLock<HashMap<String, Arc<AtomicUsize>>>>,
}

impl InFlightTracker {
    pub fn new() -> Self {
        Self::default()
    }

    /// Begin tracking an in-flight request. Returns a guard that
    /// decrements the count when dropped.
    pub fn track(&self, job_id: &str) -> InFlightGuard {
        let counter = {
            let read = self.counts.read().expect("inflight lock poisoned");
            if let Some(c) = read.get(job_id) {
                c.clone()
            } else {
                drop(read);
                let mut write = self.counts.write().expect("inflight lock poisoned");
                write
                    .entry(job_id.to_string())
                    .or_insert_with(|| Arc::new(AtomicUsize::new(0)))
                    .clone()
            }
        };
        counter.fetch_add(1, Ordering::Relaxed);
        InFlightGuard { counter }
    }

    /// Returns `true` if the given job has any in-flight requests.
    pub fn has_in_flight(&self, job_id: &str) -> bool {
        self.counts
            .read()
            .expect("inflight lock poisoned")
            .get(job_id)
            .is_some_and(|c| c.load(Ordering::Relaxed) > 0)
    }

    /// Returns the number of in-flight requests for the given job.
    pub fn count(&self, job_id: &str) -> usize {
        self.counts
            .read()
            .expect("inflight lock poisoned")
            .get(job_id)
            .map(|c| c.load(Ordering::Relaxed))
            .unwrap_or(0)
    }
}

/// RAII guard that decrements the in-flight count when dropped.
pub struct InFlightGuard {
    counter: Arc<AtomicUsize>,
}

impl Drop for InFlightGuard {
    fn drop(&mut self) {
        self.counter.fetch_sub(1, Ordering::Relaxed);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_track_and_drop() {
        let tracker = InFlightTracker::new();
        assert!(!tracker.has_in_flight("job-1"));
        assert_eq!(tracker.count("job-1"), 0);

        let guard1 = tracker.track("job-1");
        assert!(tracker.has_in_flight("job-1"));
        assert_eq!(tracker.count("job-1"), 1);

        let guard2 = tracker.track("job-1");
        assert_eq!(tracker.count("job-1"), 2);

        drop(guard1);
        assert_eq!(tracker.count("job-1"), 1);
        assert!(tracker.has_in_flight("job-1"));

        drop(guard2);
        assert_eq!(tracker.count("job-1"), 0);
        assert!(!tracker.has_in_flight("job-1"));
    }

    #[test]
    fn test_multiple_jobs() {
        let tracker = InFlightTracker::new();
        let _g1 = tracker.track("job-a");
        let _g2 = tracker.track("job-b");

        assert!(tracker.has_in_flight("job-a"));
        assert!(tracker.has_in_flight("job-b"));
        assert!(!tracker.has_in_flight("job-c"));
    }

    #[test]
    fn test_clone_shares_state() {
        let tracker = InFlightTracker::new();
        let clone = tracker.clone();

        let _guard = tracker.track("job-1");
        assert!(clone.has_in_flight("job-1"));
    }
}
