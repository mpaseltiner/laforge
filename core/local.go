package core

import (
	"fmt"

	"github.com/gen0cide/laforge/core/formatter"
)

// Local is used to represent information about the current runtime to the user
type Local struct {
	formatter.Formatable
	OS   string
	Arch string
}

func (l Local) ToString() string {
	return fmt.Sprintf(`Local
┠ OS (string)   = %s
┗ Arch (string) = %s`,
		l.OS,
		l.Arch)
}

// We have no children on a DNSRecord, so nothing to iterate on, we'll just return
func (l Local) Iter() ([]formatter.Formatable, error) {
	return []formatter.Formatable{}, nil
}

// IsWindows is a template helper function
func (l *Local) IsWindows() bool {
	return l.OS == "windows"
}

// IsMacOS is a template helper function
func (l *Local) IsMacOS() bool {
	return l.OS == "darwin"
}

// IsLinux is a template helper function
func (l *Local) IsLinux() bool {
	return l.OS == "linux"
}
