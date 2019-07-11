package provisioner

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/packer/packer"
	restart "github.com/hashicorp/packer/provisioner/windows-restart"
	"github.com/hashicorp/packer/template/interpolate"
)

// WindowsRestartProvisioner implements a communicator to interact with a host via SSH
type WindowsRestartProvisioner struct {
	Name              string
	Comm              packer.Communicator
	Config            *restart.Config
	UI                packer.Ui
	Stdin             io.Reader
	Stdout            io.Writer
	Stderr            io.Writer
	StartRetryTimeout time.Duration
	Context           *interpolate.Context
	cancel            chan struct{}
	cancelLock        sync.Mutex
}

// SetName implements the Provisioner interface
func (p *WindowsRestartProvisioner) SetName(s string) {
	p.Name = s
}

// GetName implements the Provisioner interface
func (p *WindowsRestartProvisioner) GetName() string {
	return p.Name
}

// SetUI implements the Provisioner interface
func (p *WindowsRestartProvisioner) SetUI(ui packer.Ui) {
	p.UI = ui
}

// GetUI implements the Provisioner interface
func (p *WindowsRestartProvisioner) GetUI() packer.Ui {
	return p.UI
}

// SetConfig implements the Provisioner interface
func (p *WindowsRestartProvisioner) SetConfig(c interface{}) error {
	sc, ok := c.(*restart.Config)
	if !ok {
		return errors.New("config is not of type *shell.Config")
	}
	p.Config = sc
	return p.Prepare(sc)
}

// GetConfig implements the Provisioner interface
func (p *WindowsRestartProvisioner) GetConfig() interface{} {
	return p.Config
}

// SetComms implements the Provisioner interface
func (p *WindowsRestartProvisioner) SetComms(c packer.Communicator) {
	p.Comm = c
}

// GetComms implements the Provisioner interface
func (p *WindowsRestartProvisioner) GetComms() packer.Communicator {
	return p.Comm
}

// SetIO implements the Provisioner interface
func (p *WindowsRestartProvisioner) SetIO(in io.Reader, out io.Writer, err io.Writer) {
	p.Stdin = in
	p.Stdout = out
	p.Stderr = err
}

// GetIO implements the Provisioner interface
func (p *WindowsRestartProvisioner) GetIO() (io.Reader, io.Writer, io.Writer) {
	return p.Stdin, p.Stdout, p.Stderr
}

// Prepare implements the Provisioner interface
func (p *WindowsRestartProvisioner) Prepare(raws ...interface{}) error {
	if p.Config.RestartCommand == "" {
		p.Config.RestartCommand = restart.DefaultRestartCommand
	}

	if p.Config.RestartCheckCommand == "" {
		p.Config.RestartCheckCommand = restart.DefaultRestartCheckCommand
	}

	if p.Config.RestartTimeout == 0 {
		p.Config.RestartTimeout = 5 * time.Minute
	}

	return nil
}

// Provision implements the Provisioner interface
func (p *WindowsRestartProvisioner) Provision() error {
	p.cancelLock.Lock()
	p.cancel = make(chan struct{})
	p.cancelLock.Unlock()

	p.UI.Say("Restarting Machine")

	var cmd *packer.RemoteCmd
	command := p.Config.RestartCommand
	err := p.retryable(func() error {
		cmd = &packer.RemoteCmd{Command: command}
		return cmd.StartWithUi(p.Comm, p.UI)
	})

	if err != nil {
		return err
	}

	if cmd.ExitStatus != 0 {
		return fmt.Errorf("Restart script exited with non-zero exit status: %d", cmd.ExitStatus)
	}

	return waitForRestart(p, p.Comm)
}

var waitForRestart = func(p *WindowsRestartProvisioner, comm packer.Communicator) error {
	p.UI.Say("Waiting for machine to restart...")
	waitDone := make(chan bool, 1)
	timeout := time.After(p.Config.RestartTimeout)
	var err error

	var cmd *packer.RemoteCmd
	trycommand := restart.TryCheckReboot
	abortcommand := restart.AbortReboot

	// This sleep works around an azure/winrm bug. For more info see
	// https://github.com/hashicorp/packer/issues/5257; we can remove the
	// sleep when the underlying bug has been resolved.
	time.Sleep(1 * time.Second)

	// Stolen from Vagrant reboot checker
	for {
		p.UI.Say("Check if machine is rebooting...")
		cmd = &packer.RemoteCmd{Command: trycommand}
		err = cmd.StartWithUi(p.Comm, p.UI)
		if err != nil {
			// Couldn't execute, we assume machine is rebooting already
			break
		}
		if cmd.ExitStatus == 1 {
			// SSH provisioner, and we're already rebooting. SSH can reconnect
			// without our help; exit this wait loop.
			break
		}
		if cmd.ExitStatus == 1115 || cmd.ExitStatus == 1190 || cmd.ExitStatus == 1717 {
			// Reboot already in progress but not completed
			p.UI.Say("Reboot already in progress, waiting...")
			time.Sleep(10 * time.Second)
		}
		if cmd.ExitStatus == 0 {
			// Cancel reboot we created to test if machine was already rebooting
			cmd = &packer.RemoteCmd{Command: abortcommand}
			cmd.StartWithUi(p.Comm, p.UI)
			break
		}
	}

	go func() {
		log.Printf("Waiting for machine to become available...")
		err = waitForCommunicator(p)
		waitDone <- true
	}()

	log.Printf("Waiting for machine to reboot with timeout: %s", p.Config.RestartTimeout)

WaitLoop:
	for {
		// Wait for either WinRM to become available, a timeout to occur,
		// or an interrupt to come through.
		select {
		case <-waitDone:
			if err != nil {
				p.UI.Error(fmt.Sprintf("Error waiting for machine to restart: %s", err))
				return err
			}

			p.UI.Say("Machine successfully restarted, moving on")
			close(p.cancel)
			break WaitLoop
		case <-timeout:
			err := fmt.Errorf("Timeout waiting for machine to restart")
			p.UI.Error(err.Error())
			close(p.cancel)
			return err
		case <-p.cancel:
			close(waitDone)
			return fmt.Errorf("Interrupt detected, quitting waiting for machine to restart")
		}
	}
	return nil

}

var waitForCommunicator = func(p *WindowsRestartProvisioner) error {
	runCustomRestartCheck := true
	if p.Config.RestartCheckCommand == restart.DefaultRestartCheckCommand {
		runCustomRestartCheck = false
	}
	// This command is configurable by the user to make sure that the
	// vm has met their necessary criteria for having restarted. If the
	// user doesn't set a special restart command, we just run the
	// default as cmdModuleLoad below.
	cmdRestartCheck := &packer.RemoteCmd{Command: p.Config.RestartCheckCommand}
	p.UI.Say(fmt.Sprintf("Checking that communicator is connected with: '%s'", cmdRestartCheck.Command))
	for {
		select {
		case <-p.cancel:
			p.UI.Say("Communicator wait canceled, exiting loop")
			return fmt.Errorf("Communicator wait canceled")
		case <-time.After(retryableSleep):
		}
		if runCustomRestartCheck {
			// run user-configured restart check
			err := cmdRestartCheck.StartWithUi(p.Comm, p.UI)
			if err != nil {
				p.UI.Say(fmt.Sprintf("Communication connection err: %s", err))
				continue
			}
			p.UI.Say("Connected to machine")
			runCustomRestartCheck = false
		}

		// This is the non-user-configurable check that powershell
		// modules have loaded.

		// If we catch the restart in just the right place, we will be able
		// to run the restart check but the output will be an error message
		// about how it needs powershell modules to load, and we will start
		// provisioning before powershell is actually ready.
		// In this next check, we parse stdout to make sure that the command is
		// actually running as expected.
		cmdModuleLoad := &packer.RemoteCmd{Command: restart.DefaultRestartCheckCommand}
		var buf, buf2 bytes.Buffer
		cmdModuleLoad.Stdout = &buf
		cmdModuleLoad.Stdout = io.MultiWriter(cmdModuleLoad.Stdout, &buf2)

		cmdModuleLoad.StartWithUi(p.Comm, p.UI)
		stdoutToRead := buf2.String()

		if !strings.Contains(stdoutToRead, "restarted.") {
			p.UI.Say("echo didn't succeed; retrying...")
			continue
		}
		break
	}

	return nil
}

// Cancel implements the Provisioner interface
func (p *WindowsRestartProvisioner) Cancel() {
	p.UI.Say("Received interrupt Cancel()")

	p.cancelLock.Lock()
	defer p.cancelLock.Unlock()
	if p.cancel != nil {
		close(p.cancel)
	}
}

// retryable will retry the given function over and over until a
// non-error is returned.
func (p *WindowsRestartProvisioner) retryable(f func() error) error {
	startTimeout := time.After(p.Config.RestartTimeout)
	for {
		var err error
		if err = f(); err == nil {
			return nil
		}

		// Create an error and log it
		err = fmt.Errorf("Retryable error: %s", err)
		p.UI.Error(err.Error())

		// Check if we timed out, otherwise we retry. It is safe to
		// retry since the only error case above is if the command
		// failed to START.
		select {
		case <-startTimeout:
			return err
		default:
			time.Sleep(retryableSleep)
		}
	}
}
