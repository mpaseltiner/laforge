package provisioner

import (
	"fmt"
	"io"
	"time"

	"github.com/juju/utils/filepath"

	"github.com/hashicorp/packer/packer"
	"github.com/hashicorp/packer/provisioner/file"

	"github.com/hashicorp/packer/provisioner/shell"

	winshell "github.com/hashicorp/packer/provisioner/windows-shell"

	"github.com/hashicorp/packer/provisioner/powershell"

	"github.com/hashicorp/packer/provisioner/windows-restart"
)

// WindowsFilepath is a singleton for building windows file paths
var WindowsFilepath filepath.Renderer

// UnixFilepath is a singleton for building unix file paths
var UnixFilepath filepath.Renderer

// Provisioner is an interface type that allows for remote functions to be performed
type Provisioner interface {
	SetName(string)
	GetName() string
	SetUI(packer.Ui)
	GetUI() packer.Ui
	SetConfig(interface{}) error
	GetConfig() interface{}
	SetComms(packer.Communicator)
	GetComms() packer.Communicator
	SetIO(io.Reader, io.Writer, io.Writer)
	GetIO() (io.Reader, io.Writer, io.Writer)
	Provision() error
	Prepare(...interface{}) error
	Cancel()
}

func init() {
	wfp, err := filepath.NewRenderer("windows")
	if err != nil {
		panic(err)
	}
	ufp, err := filepath.NewRenderer("linux")
	if err != nil {
		panic(err)
	}

	WindowsFilepath = wfp
	UnixFilepath = ufp
}

// WindowsRestartConfig returns a default packer configuration
func WindowsRestartConfig() *restart.Config {
	return &restart.Config{
		RestartCheckCommand: restart.DefaultRestartCheckCommand,
		RestartTimeout:      time.Duration(5 * time.Minute),
		RestartCommand:      restart.DefaultRestartCommand,
	}
}

// WindowsPowershellConfig returns a default packer configuration
func WindowsPowershellConfig(src, name string, retry int) *powershell.Config {
	return &powershell.Config{
		Script:            src,
		RemotePath:        WindowsFilepath.Join(`C:\Windows\Temp`, fmt.Sprintf("%s.ps1", name)),
		StartRetryTimeout: time.Duration(int64(retry)) * time.Second,
	}
}

// WindowsShellConfig returns a default packer configuration
func WindowsShellConfig(src, name string) *winshell.Config {
	return &winshell.Config{
		Script:     src,
		RemotePath: WindowsFilepath.Join(WindowsFilepath.Dir(winshell.DefaultRemotePath), name),
	}
}

// LinuxShellConfig returns a default packer configuration
func LinuxShellConfig(src, name string) *shell.Config {
	return &shell.Config{
		PauseAfter:       time.Duration(5 * time.Second),
		ExpectDisconnect: true,
		Script:           src,
		RemoteFolder:     "/root",
		RemoteFile:       name,
	}
}

// FileConfig returns a default packer configuration
func FileConfig(src, dst string) *file.Config {
	return &file.Config{
		Source:      src,
		Destination: dst,
	}
}
