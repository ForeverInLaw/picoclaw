package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func TestDashboardSetupFileHasProtectedOwnerOnlyACL(t *testing.T) {
	_, path, err := createDashboardSetupToken(filepath.Join(t.TempDir(), "auth.db"))
	require.NoError(t, err)
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	require.NoError(t, err)
	expected, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")")
	require.NoError(t, err)
	actual, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	require.NoError(t, err)
	require.NotNil(t, actual)
	require.Equal(t, expected.String(), actual.String())
}
