package presentation

import "time"

type FilePathRequest struct {
	// Path is absolute inside the sandbox root; symlinks resolve inside the sandbox.
	Path string `query:"path" validate:"required,max=4096" example:"/home/sandbox/main.py"`
}

type WriteFileRequest struct {
	Path string `query:"path" validate:"required,max=4096" example:"/home/sandbox/main.py"`
	// Mode is the octal permission for a newly created file.
	Mode string `query:"mode" validate:"omitempty,len=4,numeric" example:"0644"`
}

type MkdirRequest struct {
	Path string `json:"path" validate:"required,max=4096" example:"/home/sandbox/project"`
	// Parents creates missing parent directories, like mkdir -p.
	Parents bool `json:"parents" example:"true"`
	// Mode is the octal permission for created directories.
	Mode string `json:"mode" validate:"omitempty,len=4,numeric" example:"0755"`
}

type RemoveFileRequest struct {
	Path string `query:"path" validate:"required,max=4096" example:"/home/sandbox/project"`
	// Recursive removes non-empty directories.
	Recursive bool `query:"recursive" example:"false"`
}

type FileInfoResponse struct {
	Path       string    `json:"path" example:"/home/sandbox/main.py"`
	Name       string    `json:"name" example:"main.py"`
	Type       string    `json:"type" example:"file"`
	Size       int64     `json:"size" example:"128"`
	Mode       string    `json:"mode" example:"0644"`
	ModifiedAt time.Time `json:"modified_at"`
	// LinkTarget is set for symlinks; it is the raw target, resolved inside the sandbox.
	LinkTarget string `json:"link_target,omitempty"`
}
