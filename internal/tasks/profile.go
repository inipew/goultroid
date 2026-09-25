package tasks

import "time"

// ExecutionProfile describes how an already-authorized unit of work should be
// admitted to TaskEngine. It carries policy, not execution state.
type ExecutionProfile struct {
	Pool             PoolID
	Class            PriorityClass
	QueueTimeout     time.Duration
	ExecutionTimeout time.Duration
	Resources        []ResourceRequirement
}

// WithDefaults fills zero-valued scheduling fields from defaults while keeping
// explicitly declared resources owned by the profile itself.
func (p ExecutionProfile) WithDefaults(defaults ExecutionProfile) ExecutionProfile {
	if p.Pool == "" {
		p.Pool = defaults.Pool
	}
	if p.Class == "" {
		p.Class = defaults.Class
	}
	if p.QueueTimeout == 0 {
		p.QueueTimeout = defaults.QueueTimeout
	}
	if p.ExecutionTimeout == 0 {
		p.ExecutionTimeout = defaults.ExecutionTimeout
	}
	p.Resources = append([]ResourceRequirement(nil), p.Resources...)
	return p
}
