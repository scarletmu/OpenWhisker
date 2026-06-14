package clip

// Job is one unit of background clip work: fetch URL, extract, and rewrite the
// already-created skeleton note at NotePath.
type Job struct {
	NotePath    string
	URL         string
	ParentJobID string
}

// Queue is an in-process, non-durable hand-off from the capture path to the
// clip worker. Durability is provided separately by the worker's periodic scan
// of status: clipping notes, so a dropped (buffer-full) or lost-on-restart job
// is recovered from the vault rather than from this queue. This is the
// deliberate trade chosen over a sqlite-backed queue: no schema change, and the
// vault note's own status field is the source of truth.
type Queue struct {
	ch chan Job
}

// NewQueue returns a buffered queue. A non-positive buffer defaults to 64.
func NewQueue(buffer int) *Queue {
	if buffer <= 0 {
		buffer = 64
	}
	return &Queue{ch: make(chan Job, buffer)}
}

// Enqueue offers a job without blocking. It returns false when the buffer is
// full; the caller must never block the instant capture ack on clip work, so a
// dropped job is left for the worker's scan to recover.
func (q *Queue) Enqueue(job Job) bool {
	select {
	case q.ch <- job:
		return true
	default:
		return false
	}
}

// C exposes the receive side for the worker loop.
func (q *Queue) C() <-chan Job {
	return q.ch
}
