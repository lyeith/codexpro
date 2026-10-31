package jobs

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/lyeith/codexpro/go/internal/process"
)

// readOwnerMetadata never waits on a FIFO or follows a leaf symlink. Both the
// specification and grant are private, bounded, regular ownership records.
func (s *Service) readOwnerMetadata(path string, limit int64) ([]byte, error) {
	if !filepath.IsAbs(path) || !process.Within(s.dir, path) {
		return nil, errors.New("ownership metadata escapes private job state")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("ownership metadata is not a bounded regular file")
	}
	body, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errors.New("ownership metadata exceeds its byte limit")
	}
	return body, nil
}

// executionGranted distinguishes a committed launch record from a published
// execution grant: start commits LaunchState="granted" before writing the
// nonce file. A crash in that window still requires registration revocation.
func (s *Service) executionGranted(record Record) bool {
	if record.LaunchState != "granted" {
		return false
	}
	body, err := s.readOwnerMetadata(record.SpecPath, 4*1024*1024)
	if err != nil {
		return false
	}
	var spec process.Spec
	if json.Unmarshal(body, &spec) != nil || spec.Validate(record.SpecPath) != nil || spec.Grant != record.GrantPath {
		return false
	}
	grant, err := s.readOwnerMetadata(record.GrantPath, 48)
	return err == nil && len(grant) == len(spec.Nonce) && subtle.ConstantTimeCompare(grant, []byte(spec.Nonce)) == 1
}

func (s *Service) settleResources(record Record, exitCode int) error {
	if record.ResourceClaim == nil {
		return nil
	}
	if !s.executionGranted(record) {
		// finish(absent) can be truthfully quiescent before an older begin
		// reaches the helper. Cancel revokes the persisted caller registration
		// so a late begin cannot allocate after we release ownership.
		_, err := process.CancelResources(s.ctx, *record.ResourceClaim)
		return err
	}
	if _, err := process.FinishResources(s.ctx, *record.ResourceClaim, exitCode); err == nil {
		return nil
	}
	_, err := process.CancelResources(s.ctx, *record.ResourceClaim)
	return err
}
