package acpclient

import "time"

// SetDrainTimeout overrides drainTimeout for c.
func SetDrainTimeout(c *Conn, d time.Duration) { c.drainNS.Store(int64(d)) }
