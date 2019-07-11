package provisioner

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"os"
	"strings"
	"time"

	"github.com/hashicorp/packer/packer"
	"github.com/hashicorp/packer/provisioner/shell"
	"github.com/hashicorp/packer/template/interpolate"
)

/*

multistep.BasicStateBag
-> "ui" => packer.Ui
=> "communicator" => packer.Communicator
=> "hook" => packer.Hook

packer.HookedProvisioner =>
	Provisioner => packer.Provisioner
	Config => interface{}
	TypeName => string

packer.ProvisionHook => (implements Hook)
	Provisioners []*HookedProvisioner


*/

// SSHProvisioner implements a communicator to interact with a host via SSH
type SSHProvisioner struct {
	Name              string
	Comm              packer.Communicator
	Config            *shell.Config
	UI                packer.Ui
	Stdin             io.Reader
	Stdout            io.Writer
	Stderr            io.Writer
	StartRetryTimeout time.Duration
	Context           *interpolate.Context
}

// SetName implements the Provisioner interface
func (p *SSHProvisioner) SetName(s string) {
	p.Name = s
}

// GetName implements the Provisioner interface
func (p *SSHProvisioner) GetName() string {
	return p.Name
}

// SetUI implements the Provisioner interface
func (p *SSHProvisioner) SetUI(ui packer.Ui) {
	p.UI = ui
}

// GetUI implements the Provisioner interface
func (p *SSHProvisioner) GetUI() packer.Ui {
	return p.UI
}

// SetConfig implements the Provisioner interface
func (p *SSHProvisioner) SetConfig(c interface{}) error {
	sc, ok := c.(*shell.Config)
	if !ok {
		return errors.New("config is not of type *shell.Config")
	}
	p.Config = sc
	return p.Prepare(sc)
}

// GetConfig implements the Provisioner interface
func (p *SSHProvisioner) GetConfig() interface{} {
	return p.Config
}

// SetComms implements the Provisioner interface
func (p *SSHProvisioner) SetComms(c packer.Communicator) {
	p.Comm = c
}

// GetComms implements the Provisioner interface
func (p *SSHProvisioner) GetComms() packer.Communicator {
	return p.Comm
}

// SetIO implements the Provisioner interface
func (p *SSHProvisioner) SetIO(in io.Reader, out io.Writer, err io.Writer) {
	p.Stdin = in
	p.Stdout = out
	p.Stderr = err
}

// GetIO implements the Provisioner interface
func (p *SSHProvisioner) GetIO() (io.Reader, io.Writer, io.Writer) {
	return p.Stdin, p.Stdout, p.Stderr
}

// Prepare ensures proper configuration with the SSH Provisioner
func (p *SSHProvisioner) Prepare(raws ...interface{}) error {
	if p.Config.ExecuteCommand == "" {
		p.Config.ExecuteCommand = "chmod +x {{.Path}}; {{.Vars}} {{.Path}}"
		if p.Config.UseEnvVarFile == true {
			p.Config.ExecuteCommand = "chmod +x {{.Path}}; . {{.EnvVarFile}} && {{.Path}}"
		}
	}

	if p.Config.Inline != nil && len(p.Config.Inline) == 0 {
		p.Config.Inline = nil
	}

	if p.Config.InlineShebang == "" {
		p.Config.InlineShebang = "/bin/sh -e"
	}

	if p.Config.RawStartRetryTimeout == "" {
		p.Config.RawStartRetryTimeout = "5m"
	}

	if p.Config.RemoteFolder == "" {
		p.Config.RemoteFolder = "/tmp"
	}

	if p.Config.RemoteFile == "" {
		p.Config.RemoteFile = fmt.Sprintf("script_%d.sh", rand.Intn(9999))
	}

	if p.Config.RemotePath == "" {
		p.Config.RemotePath = fmt.Sprintf("%s/%s", p.Config.RemoteFolder, p.Config.RemoteFile)
	}

	if p.Config.Scripts == nil {
		p.Config.Scripts = make([]string, 0)
	}

	if p.Config.Vars == nil {
		p.Config.Vars = make([]string, 0)
	}

	var errs *packer.MultiError
	if p.Config.Script != "" && len(p.Config.Scripts) > 0 {
		errs = packer.MultiErrorAppend(errs, errors.New("only one of script or scripts can be specified"))
	}

	if p.Config.Script != "" {
		p.Config.Scripts = []string{p.Config.Script}
	}

	if len(p.Config.Scripts) == 0 && p.Config.Inline == nil {
		errs = packer.MultiErrorAppend(errs, errors.New("either a script file or inline script must be specified"))
	} else if len(p.Config.Scripts) > 0 && p.Config.Inline != nil {
		errs = packer.MultiErrorAppend(errs, errors.New("only a script file or an inline script can be specified, not both"))
	}

	for _, path := range p.Config.Scripts {
		if _, err := os.Stat(path); err != nil {
			errs = packer.MultiErrorAppend(errs, fmt.Errorf("Bad script '%s': %s", path, err))
		}
	}

	// Do a check for bad environment variables, such as '=foo', 'foobar'
	for _, kv := range p.Config.Vars {
		vs := strings.SplitN(kv, "=", 2)
		if len(vs) != 2 || vs[0] == "" {
			errs = packer.MultiErrorAppend(errs, fmt.Errorf("Environment variable not in format 'key=value': %s", kv))
		}
	}

	if p.Config.RawStartRetryTimeout != "" {
		str, err := time.ParseDuration(p.Config.RawStartRetryTimeout)
		if err != nil {
			errs = packer.MultiErrorAppend(errs, fmt.Errorf("Failed parsing start_retry_timeout: %s", err))
		}
		p.StartRetryTimeout = str
	} else {
		p.StartRetryTimeout = time.Duration(6 * time.Minute)
	}

	if p.Config.RawPauseAfter != "" {
		rpa, err := time.ParseDuration(p.Config.RawPauseAfter)
		if err != nil {
			errs = packer.MultiErrorAppend(errs, fmt.Errorf("Failed parsing pause_after: %s", err))
		}
		p.Config.PauseAfter = rpa
	} else {
		p.Config.PauseAfter = time.Duration(5 * time.Second)
	}

	if errs != nil && len(errs.Errors) > 0 {
		return errs
	}

	return nil
}

// Provision actually deploys the provisioner
func (p *SSHProvisioner) Provision() error {
	scripts := make([]string, len(p.Config.Scripts))
	copy(scripts, p.Config.Scripts)

	if p.Config.Inline != nil {
		tf, err := ioutil.TempFile("", "packer-shell")
		if err != nil {
			return fmt.Errorf("Error preparing shell script: %s", err)
		}
		defer os.Remove(tf.Name())

		scripts = append(scripts, tf.Name())

		writer := bufio.NewWriter(tf)
		writer.WriteString(fmt.Sprintf("#!%s\n", p.Config.InlineShebang))
		for _, command := range p.Config.Inline {
			if _, err := writer.WriteString(command + "\n"); err != nil {
				return fmt.Errorf("Error preparing shell script: %s", err)
			}
		}

		if err := writer.Flush(); err != nil {
			return fmt.Errorf("Error preparing shell script: %s", err)
		}

		tf.Close()
	}

	for _, path := range scripts {
		p.UI.Say(fmt.Sprintf("Provisioning with shell script: %s", path))

		log.Printf("Opening %s for reading", path)
		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("Error opening shell script: %s", err)
		}
		defer f.Close()

		p.Context.Data = &shell.ExecuteCommandTemplate{
			Path: p.Config.RemotePath,
		}

		command, err := interpolate.Render(p.Config.ExecuteCommand, p.Context)
		if err != nil {
			return fmt.Errorf("Error processing command: %s", err)
		}

		var cmd *packer.RemoteCmd
		err = p.retryable(func() error {
			if _, err := f.Seek(0, 0); err != nil {
				return err
			}

			var r io.Reader = f
			if !p.Config.Binary {
				r = &shell.UnixReader{Reader: r}
			}

			if err := p.Comm.Upload(p.Config.RemotePath, r, nil); err != nil {
				return fmt.Errorf("Error uploading script: %s", err)
			}

			cmd = &packer.RemoteCmd{
				Stdin:   p.Stdin,
				Stdout:  p.Stdout,
				Stderr:  p.Stderr,
				Command: fmt.Sprintf("chmod 0755 %s", p.Config.RemotePath),
			}
			debugLine := fmt.Sprintf("%v - %s", time.Now(), cmd.Command)
			fmt.Fprintf(p.Stdout, "##### >>> %s\n", debugLine)
			fmt.Fprintf(p.Stderr, "##### >>> %s\n", debugLine)
			if err := p.Comm.Start(cmd); err != nil {
				return fmt.Errorf("Error chmodding script file to 0755 in remote machine: %s", err)
			}
			cmd.Wait()

			cmd = &packer.RemoteCmd{
				Stdin:   p.Stdin,
				Stdout:  p.Stdout,
				Stderr:  p.Stderr,
				Command: command,
			}
			debugLine = fmt.Sprintf("%v - %s", time.Now(), cmd.Command)
			fmt.Fprintf(p.Stdout, "##### >>> %s\n", debugLine)
			fmt.Fprintf(p.Stderr, "##### >>> %s\n", debugLine)
			return cmd.StartWithUi(p.Comm, p.UI)
		})

		if err != nil {
			return err
		}

		if cmd.ExitStatus == packer.CmdDisconnect {
			if !p.Config.ExpectDisconnect {
				return fmt.Errorf("script disconnected unexpectedly. If you expected your script to disconnect, i.e. from a restart, you can try adding `expect_disconnect = true` to the laforge script parameters")
			}
		} else if cmd.ExitStatus != 0 {
			return fmt.Errorf("Script exited with non-zero exit status: %d", cmd.ExitStatus)
		}

		if !p.Config.SkipClean {
			err = p.cleanupRemoteFile(p.Config.RemotePath, p.Comm)
			if err != nil {
				return err
			}
		}
	}

	if p.Config.RawPauseAfter != "" {
		p.UI.Say(fmt.Sprintf("Pausing %s after this provisioner...", p.Config.PauseAfter))
		select {
		case <-time.After(p.Config.PauseAfter):
			return nil
		}
	}

	return nil
}

func (p *SSHProvisioner) cleanupRemoteFile(path string, comm packer.Communicator) error {
	err := p.retryable(func() error {
		cmd := &packer.RemoteCmd{
			Stdin:   p.Stdin,
			Stdout:  p.Stdout,
			Stderr:  p.Stderr,
			Command: fmt.Sprintf("rm -f %s", path),
		}
		debugLine := fmt.Sprintf("%v - %s", time.Now(), cmd.Command)
		fmt.Fprintf(p.Stdout, "##### >>> %s\n", debugLine)
		fmt.Fprintf(p.Stderr, "##### >>> %s\n", debugLine)
		if err := comm.Start(cmd); err != nil {
			return fmt.Errorf(
				"Error removing temporary script at %s: %s",
				path, err)
		}
		cmd.Wait()
		// treat disconnects as retryable by returning an error
		if cmd.ExitStatus == packer.CmdDisconnect {
			return fmt.Errorf("disconnect while removing temporary script")
		}
		if cmd.ExitStatus != 0 {
			return fmt.Errorf("error removing temporary script at %s", path)
		}
		return nil
	})

	if err != nil {
		return err
	}

	return nil
}

// Cancel effectively NOOPs the provisioner
func (p *SSHProvisioner) Cancel() {
	return
}

// retryable will retry the given function over and over until a
// non-error is returned.
func (p *SSHProvisioner) retryable(f func() error) error {
	startTimeout := time.After(p.StartRetryTimeout)
	for {
		var err error
		if err = f(); err == nil {
			return nil
		}

		err = fmt.Errorf("Retryable error: %s", err)
		log.Print(err.Error())

		select {
		case <-startTimeout:
			return err
		default:
			time.Sleep(2 * time.Second)
		}
	}
}
