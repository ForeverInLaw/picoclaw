package skills

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/sipeed/picoclaw/pkg/utils"
)

var (
	ErrSkillAlreadyInstalled = errors.New("skill is already installed; use force=true to reinstall")
	ErrSkillUnsafeDirectory  = errors.New("skill directory must not be a symbolic link")
	ErrSkillInstallStorage   = errors.New("cannot prepare the skill install; check workspace permissions")
	ErrSkillRegistryMissing  = errors.New("registry not found")
	ErrSkillDownload         = errors.New(
		"skill download or archive validation failed; try again or choose another skill",
	)
	ErrSkillMalware        = errors.New("skill is flagged as malicious and cannot be installed")
	ErrSkillInvalidArchive = errors.New("registry archive does not contain a valid SKILL.md")
	ErrSkillProvenance     = errors.New("cannot record skill provenance; check workspace permissions")
	ErrSkillActivation     = errors.New(
		"cannot activate the skill; check workspace permissions and existing installs",
	)
	workspaceSkillLocks sync.Map
)

// LockWorkspaceSkills serializes in-process install/import/delete mutations.
// Waiting respects cancellation; aliases of an existing workspace share a lock.
func LockWorkspaceSkills(ctx context.Context, workspace string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key, err := filepath.Abs(workspace)
	if err != nil {
		return nil, ErrSkillInstallStorage
	}
	if resolved, resolveErr := filepath.EvalSymlinks(key); resolveErr == nil {
		key = resolved
	}
	value, _ := workspaceSkillLocks.LoadOrStore(key, make(chan struct{}, 1))
	lock := value.(chan struct{})
	select {
	case lock <- struct{}{}:
		return func() { <-lock }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// InstallRegistrySkill stages and validates a registry archive before activation.
// Force replaces an existing skill only after the new skill is ready. Callers
// keep their transport-specific validation and success/error presentation.
func InstallRegistrySkill(
	ctx context.Context,
	workspace string,
	registry SkillRegistry,
	slug, version string,
	force bool,
) (*InstallResult, error) {
	if err := utils.ValidateSkillIdentifier(slug); err != nil {
		return nil, err
	}
	unlock, err := LockWorkspaceSkills(ctx, workspace)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	root := filepath.Join(workspace, "skills")
	target := filepath.Join(root, slug)
	if _, err = os.Lstat(target); err == nil {
		if !force {
			return nil, ErrSkillAlreadyInstalled
		}
	} else if !os.IsNotExist(err) {
		return nil, ErrSkillInstallStorage
	}
	if registry == nil {
		return nil, ErrSkillRegistryMissing
	}
	if info, statErr := os.Lstat(root); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrSkillUnsafeDirectory
	}
	if os.MkdirAll(root, 0o755) != nil {
		return nil, ErrSkillInstallStorage
	}
	stage, err := os.MkdirTemp(root, ".skill-install-*")
	if err != nil {
		return nil, ErrSkillInstallStorage
	}
	defer os.RemoveAll(stage)
	stagedTarget := filepath.Join(stage, "skills", slug)
	result, err := registry.DownloadAndInstall(ctx, slug, version, stagedTarget)
	if err != nil {
		if canceled := ctx.Err(); canceled != nil {
			return nil, canceled
		}
		return nil, ErrSkillDownload
	}
	if result == nil {
		return nil, ErrSkillInvalidArchive
	}
	if result.IsMalwareBlocked {
		return nil, ErrSkillMalware
	}
	if len(NewSkillsLoader(stage, "", "").ListSkills()) != 1 {
		return nil, ErrSkillInvalidArchive
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if WriteOriginMetadata(stagedTarget, registry.Name(), slug, result.Version) != nil {
		return nil, ErrSkillProvenance
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if force && os.RemoveAll(target) != nil {
		return nil, ErrSkillActivation
	}
	if os.Rename(stagedTarget, target) != nil {
		return nil, ErrSkillActivation
	}
	return result, nil
}
