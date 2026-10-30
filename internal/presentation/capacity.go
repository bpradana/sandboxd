package presentation

// CapacityResponse is what a placement layer needs to choose a host: who this
// daemon is, whether it can build anything, how many sandboxes it will still
// take, what it has already promised, and what the machine underneath
// actually has. The last two are separate on purpose -- a daemon can be
// configured to promise more than its host can deliver, and the decision to
// allow that belongs to whoever is placing, not here.
type CapacityResponse struct {
	Node         string `json:"node" example:"sandboxd-1"`
	Architecture string `json:"architecture" example:"arm64"`
	// Ready is false when the engine failed its preflight checks, in which
	// case this host can report capacity it cannot actually use.
	Ready bool `json:"ready" example:"true"`
	// Snapshots reports whether cloud storage is configured. A host without
	// it cannot materialize an image it does not already have.
	Snapshots bool                 `json:"snapshots" example:"true"`
	Sandboxes SandboxSlotsResponse `json:"sandboxes"`
	Allocated AllocatedResponse    `json:"allocated"`
	Host      HostCapacityResponse `json:"host"`
	Images    []string             `json:"images" example:"alpine,python-ready"`
}

type SandboxSlotsResponse struct {
	Active int `json:"active" example:"3"`
	Max    int `json:"max" example:"64"`
}

// AllocatedResponse sums the limits of every sandbox currently running here.
type AllocatedResponse struct {
	CPUMillis int `json:"cpu_millis" example:"3000"`
	MemoryMB  int `json:"memory_mb" example:"1536"`
	StorageMB int `json:"storage_mb" example:"1536"`
}

// HostCapacityResponse is read from the kernel, not from configuration.
type HostCapacityResponse struct {
	CPUMillis int `json:"cpu_millis" example:"8000"`
	MemoryMB  int `json:"memory_mb" example:"16384"`
	// MemoryAvailableMB is the kernel's estimate of what can still be
	// allocated without swapping, not simply unused memory.
	MemoryAvailableMB int `json:"memory_available_mb" example:"11020"`
	StorageTotalMB    int `json:"storage_total_mb" example:"65536"`
	StorageFreeMB     int `json:"storage_free_mb" example:"51200"`
}

type CapacityEnvelopeDoc = EnvelopeDoc[CapacityResponse]
