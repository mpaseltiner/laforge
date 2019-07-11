package provisioner

import (
	"bufio"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"strings"
	"text/template"
	"time"

	"github.com/hashicorp/packer/provisioner/powershell"

	"github.com/hashicorp/packer/common/uuid"
	"github.com/hashicorp/packer/packer"
	"github.com/hashicorp/packer/template/interpolate"
)

var retryableSleep = 2 * time.Second

var psEscape = strings.NewReplacer(
	"$", "`$",
	"\"", "`\"",
	"`", "``",
	"'", "`'",
)

// PowershellProvisioner implements a communicator to interact with a host via SSH
type PowershellProvisioner struct {
	Name              string
	Comm              packer.Communicator
	Config            *powershell.Config
	UI                packer.Ui
	Stdin             io.Reader
	Stdout            io.Writer
	Stderr            io.Writer
	StartRetryTimeout time.Duration
	Context           *interpolate.Context
}

// SetName implements the Provisioner interface
func (p *PowershellProvisioner) SetName(s string) {
	p.Name = s
}

// GetName implements the Provisioner interface
func (p *PowershellProvisioner) GetName() string {
	return p.Name
}

// SetUI implements the Provisioner interface
func (p *PowershellProvisioner) SetUI(ui packer.Ui) {
	p.UI = ui
}

// GetUI implements the Provisioner interface
func (p *PowershellProvisioner) GetUI() packer.Ui {
	return p.UI
}

// SetConfig implements the Provisioner interface
func (p *PowershellProvisioner) SetConfig(c interface{}) error {
	sc, ok := c.(*powershell.Config)
	if !ok {
		return errors.New("config is not of type *powershell.Config")
	}
	p.Config = sc
	return p.Prepare(sc)
}

// GetConfig implements the Provisioner interface
func (p *PowershellProvisioner) GetConfig() interface{} {
	return p.Config
}

// SetComms implements the Provisioner interface
func (p *PowershellProvisioner) SetComms(c packer.Communicator) {
	p.Comm = c
}

// GetComms implements the Provisioner interface
func (p *PowershellProvisioner) GetComms() packer.Communicator {
	return p.Comm
}

// SetIO implements the Provisioner interface
func (p *PowershellProvisioner) SetIO(in io.Reader, out io.Writer, err io.Writer) {
	p.Stdin = in
	p.Stdout = out
	p.Stderr = err
}

// GetIO implements the Provisioner interface
func (p *PowershellProvisioner) GetIO() (io.Reader, io.Writer, io.Writer) {
	return p.Stdin, p.Stdout, p.Stderr
}

// ExecuteCommandTemplate is used by packer's rendering engine
type ExecuteCommandTemplate struct {
	Vars          string
	Path          string
	WinRMPassword string
}

// EnvVarsTemplate is used by packer's rendering engine
type EnvVarsTemplate struct {
	WinRMPassword string
}

// Prepare implements the Provisioner interface
func (p *PowershellProvisioner) Prepare(raws ...interface{}) error {
	if p.Config.EnvVarFormat == "" {
		p.Config.EnvVarFormat = `$env:%s="%s"; `
	}

	if p.Config.ElevatedEnvVarFormat == "" {
		p.Config.ElevatedEnvVarFormat = `$env:%s="%s"; `
	}

	if p.Config.ExecuteCommand == "" {
		p.Config.ExecuteCommand = `powershell -noprofile -executionpolicy bypass "& { if (Test-Path variable:global:ProgressPreference){set-variable -name variable:global:ProgressPreference -value 'SilentlyContinue'}; &'{{.Path}}'; exit $LastExitCode }"`
	}

	if p.Config.ElevatedExecuteCommand == "" {
		p.Config.ElevatedExecuteCommand = `powershell -noprofile -executionpolicy bypass "& { if (Test-Path variable:global:ProgressPreference){set-variable -name variable:global:ProgressPreference -value 'SilentlyContinue'}; &'{{.Path}}'; exit $LastExitCode }"`
	}

	if p.Config.Inline != nil && len(p.Config.Inline) == 0 {
		p.Config.Inline = nil
	}

	if p.Config.StartRetryTimeout == 0 {
		p.Config.StartRetryTimeout = 5 * time.Minute
	}

	if p.Config.RemotePath == "" {
		uuid := uuid.TimeOrderedUUID()
		p.Config.RemotePath = fmt.Sprintf(`c:/Windows/Temp/script-%s.ps1`, uuid)
	}

	if p.Config.RemoteEnvVarPath == "" {
		uuid := uuid.TimeOrderedUUID()
		p.Config.RemoteEnvVarPath = fmt.Sprintf(`c:/Windows/Temp/packer-ps-env-vars-%s.ps1`, uuid)
	}

	if p.Config.Scripts == nil {
		p.Config.Scripts = make([]string, 0)
	}

	if p.Config.Vars == nil {
		p.Config.Vars = make([]string, 0)
	}

	if p.Config.ValidExitCodes == nil {
		p.Config.ValidExitCodes = []int{0}
	}

	var errs error
	if p.Config.Script != "" && len(p.Config.Scripts) > 0 {
		errs = packer.MultiErrorAppend(errs, errors.New("only one of script or scripts can be specified"))
	}

	if p.Config.ElevatedUser != "" && p.Config.ElevatedPassword == "" {
		errs = packer.MultiErrorAppend(errs, errors.New("Must supply an 'elevated_password' if 'elevated_user' provided"))
	}

	if p.Config.ElevatedUser == "" && p.Config.ElevatedPassword != "" {
		errs = packer.MultiErrorAppend(errs, errors.New("Must supply an 'elevated_user' if 'elevated_password' provided"))
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

	if errs != nil {
		return errs
	}

	return nil
}

// Takes the inline scripts, concatenates them into a temporary file and
// returns a string containing the location of said file.
func extractPowershellScript(p *PowershellProvisioner) (string, error) {
	temp, err := ioutil.TempFile(os.TempDir(), "packer-powershell-provisioner")
	if err != nil {
		return "", err
	}
	defer temp.Close()
	writer := bufio.NewWriter(temp)
	for _, command := range p.Config.Inline {
		log.Printf("Found command: %s", command)
		if _, err := writer.WriteString(command + "\n"); err != nil {
			return "", fmt.Errorf("Error preparing powershell script: %s", err)
		}
	}

	if err := writer.Flush(); err != nil {
		return "", fmt.Errorf("Error preparing powershell script: %s", err)
	}

	return temp.Name(), nil
}

// Provision implements the Provisioner interface
func (p *PowershellProvisioner) Provision() error {
	p.UI.Say(fmt.Sprintf("Provisioning with Powershell..."))

	scripts := make([]string, len(p.Config.Scripts))
	copy(scripts, p.Config.Scripts)

	if p.Config.Inline != nil {
		temp, err := extractPowershellScript(p)
		if err != nil {
			p.UI.Error(fmt.Sprintf("Unable to extract inline scripts into a file: %s", err))
		}
		scripts = append(scripts, temp)
		defer os.Remove(temp)
	}

	for _, path := range scripts {
		p.UI.Say(fmt.Sprintf("Provisioning with powershell script: %s", path))

		log.Printf("Opening %s for reading", path)
		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("Error opening powershell script: %s", err)
		}
		defer f.Close()

		command, err := p.createCommandText()
		if err != nil {
			return fmt.Errorf("Error processing command: %s", err)
		}

		// Upload the file and run the command. Do this in the context of a
		// single retryable function so that we don't end up with the case
		// that the upload succeeded, a restart is initiated, and then the
		// command is executed but the file doesn't exist any longer.
		var cmd *packer.RemoteCmd
		err = p.retryable(func() error {
			if _, err := f.Seek(0, 0); err != nil {
				return err
			}
			if err := p.Comm.Upload(p.Config.RemotePath, f, nil); err != nil {
				return fmt.Errorf("Error uploading script: %s", err)
			}

			cmd = &packer.RemoteCmd{
				Stdin:   p.Stdin,
				Stdout:  p.Stdout,
				Stderr:  p.Stderr,
				Command: command,
			}
			debugLine := fmt.Sprintf("%v - %s", time.Now(), cmd.Command)
			fmt.Fprintf(p.Stdout, "##### >>> %s\n", debugLine)
			fmt.Fprintf(p.Stderr, "##### >>> %s\n", debugLine)
			return cmd.StartWithUi(p.Comm, p.UI)
		})

		if err != nil {
			return err
		}

		// Close the original file since we copied it
		f.Close()

		// Check exit code against allowed codes (likely just 0)
		validExitCode := false
		for _, v := range p.Config.ValidExitCodes {
			if cmd.ExitStatus == v {
				validExitCode = true
			}
		}
		if !validExitCode {
			return fmt.Errorf("Script exited with non-zero exit status: %d. Allowed exit codes are: %v", cmd.ExitStatus, p.Config.ValidExitCodes)
		}

		err = p.cleanupRemoteFile(p.Config.RemotePath, p.Comm)
		if err != nil {
			return err
		}
	}

	return nil
}

// Cancel implements the Provisioner interface
func (p *PowershellProvisioner) Cancel() {
	return
}

func (p *PowershellProvisioner) cleanupRemoteFile(path string, comm packer.Communicator) error {
	err := p.retryable(func() error {
		cmd := &packer.RemoteCmd{
			Stdin:   p.Stdin,
			Stdout:  p.Stdout,
			Stderr:  p.Stderr,
			Command: fmt.Sprintf("del /f %s", path),
		}
		debugLine := fmt.Sprintf("%v - %s", time.Now(), cmd.Command)
		fmt.Fprintf(p.Stdout, "##### >>> %s\n", debugLine)
		fmt.Fprintf(p.Stderr, "##### >>> %s\n", debugLine)
		if err := comm.Start(cmd); err != nil {
			return fmt.Errorf("Error removing temporary script at %s: %s", path, err)
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

// retryable will retry the given function over and over until a non-error is
// returned.
func (p *PowershellProvisioner) retryable(f func() error) error {
	startTimeout := time.After(p.Config.StartRetryTimeout)
	for {
		var err error
		if err = f(); err == nil {
			return nil
		}

		// Create an error and log it
		err = fmt.Errorf("Retryable error: %s", err)
		log.Print(err.Error())

		// Check if we timed out, otherwise we retry. It is safe to retry
		// since the only error case above is if the command failed to START.
		select {
		case <-startTimeout:
			return err
		default:
			time.Sleep(retryableSleep)
		}
	}
}

func (p *PowershellProvisioner) createCommandText() (command string, err error) {
	if p.Config.ElevatedUser == "" {
		return p.createCommandTextNonPrivileged()
	}
	return p.createCommandTextPrivileged()
}

func (p *PowershellProvisioner) createCommandTextNonPrivileged() (command string, err error) {
	// Prepare everything needed to enable the required env vars within the
	// remote environment

	p.Context.Data = &ExecuteCommandTemplate{
		Path:          p.Config.RemotePath,
		Vars:          p.Config.RemoteEnvVarPath,
		WinRMPassword: p.Config.ElevatedPassword,
	}
	command, err = interpolate.Render(p.Config.ExecuteCommand, p.Context)

	if err != nil {
		return "", fmt.Errorf("Error processing command: %s", err)
	}

	// Return the interpolated command
	return command, nil
}

func (p *PowershellProvisioner) createCommandTextPrivileged() (command string, err error) {
	p.Context.Data = &ExecuteCommandTemplate{
		Path:          p.Config.RemotePath,
		Vars:          p.Config.RemoteEnvVarPath,
		WinRMPassword: p.Config.ElevatedPassword,
	}
	command, err = interpolate.Render(p.Config.ElevatedExecuteCommand, p.Context)
	if err != nil {
		return "", fmt.Errorf("Error processing command: %s", err)
	}

	// OK so we need an elevated shell runner to wrap our command, this is
	// going to have its own path generate the script and update the command
	// runner in the process
	path, err := p.generateElevatedRunner(command)
	if err != nil {
		return "", fmt.Errorf("Error generating elevated runner: %s", err)
	}

	// Return the path to the elevated shell wrapper
	command = fmt.Sprintf("powershell -noprofile -executionpolicy bypass -file \"%s\"", path)

	return command, err
}

func (p *PowershellProvisioner) generateElevatedRunner(command string) (uploadedPath string, err error) {
	log.Printf("Building elevated command wrapper for: %s", command)

	var buffer bytes.Buffer

	// Output from the elevated command cannot be returned directly to the
	// Packer console. In order to be able to view output from elevated
	// commands and scripts an indirect approach is used by which the commands
	// output is first redirected to file. The output file is then 'watched'
	// by Packer while the elevated command is running and any content
	// appearing in the file is written out to the console.  Below the portion
	// of command required to redirect output from the command to file is
	// built and appended to the existing command string
	taskName := fmt.Sprintf("packer-%s", uuid.TimeOrderedUUID())
	// Only use %ENVVAR% format for environment variables when setting the log
	// file path; Do NOT use $env:ENVVAR format as it won't be expanded
	// correctly in the elevatedTemplate
	logFile := `%SYSTEMROOT%/Temp/` + taskName + ".out"
	command += fmt.Sprintf(" > %s 2>&1", logFile)

	// elevatedTemplate wraps the command in a single quoted XML text string
	// so we need to escape characters considered 'special' in XML.
	err = xml.EscapeText(&buffer, []byte(command))
	if err != nil {
		return "", fmt.Errorf("Error escaping characters special to XML in command %s: %s", command, err)
	}
	escapedCommand := buffer.String()
	log.Printf("Command [%s] converted to [%s] for use in XML string", command, escapedCommand)
	buffer.Reset()

	// Escape chars special to PowerShell in the ElevatedUser string
	escapedElevatedUser := psEscape.Replace(p.Config.ElevatedUser)
	if escapedElevatedUser != p.Config.ElevatedUser {
		log.Printf("Elevated user %s converted to %s after escaping chars special to PowerShell",
			p.Config.ElevatedUser, escapedElevatedUser)
	}
	// Replace ElevatedPassword for winrm users who used this feature
	p.Context.Data = &EnvVarsTemplate{
		WinRMPassword: p.Config.ElevatedPassword,
	}

	p.Config.ElevatedPassword, _ = interpolate.Render(p.Config.ElevatedPassword, p.Context)

	// Escape chars special to PowerShell in the ElevatedPassword string
	escapedElevatedPassword := psEscape.Replace(p.Config.ElevatedPassword)
	if escapedElevatedPassword != p.Config.ElevatedPassword {
		log.Printf("Elevated password %s converted to %s after escaping chars special to PowerShell", p.Config.ElevatedPassword, escapedElevatedPassword)
	}

	// Generate command
	err = elevatedTemplate.Execute(&buffer, elevatedOptions{
		User:              escapedElevatedUser,
		Password:          escapedElevatedPassword,
		TaskName:          taskName,
		TaskDescription:   "Packer elevated task",
		LogFile:           logFile,
		XMLEscapedCommand: escapedCommand,
	})

	if err != nil {
		fmt.Printf("Error creating elevated template: %s", err)
		return "", err
	}
	uuid := uuid.TimeOrderedUUID()
	path := fmt.Sprintf(`C:/Windows/Temp/packer-elevated-shell-%s.ps1`, uuid)
	log.Printf("Uploading elevated shell wrapper for command [%s] to [%s]", command, path)
	err = p.Comm.Upload(path, &buffer, nil)
	if err != nil {
		return "", fmt.Errorf("Error preparing elevated powershell script: %s", err)
	}
	return path, err
}

type elevatedOptions struct {
	User              string
	Password          string
	TaskName          string
	TaskDescription   string
	LogFile           string
	XMLEscapedCommand string
}

var elevatedTemplate = template.Must(template.New("ElevatedCommand").Parse(`
$name = "{{.TaskName}}"
$log = [System.Environment]::ExpandEnvironmentVariables("{{.LogFile}}")
$s = New-Object -ComObject "Schedule.Service"
$s.Connect()
$t = $s.NewTask($null)
$t.XmlText = @'
<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
	<Description>{{.TaskDescription}}</Description>
  </RegistrationInfo>
  <Principals>
    <Principal id="Author">
      <UserId>{{.User}}</UserId>
      <LogonType>Password</LogonType>
      <RunLevel>HighestAvailable</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>false</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT24H</ExecutionTimeLimit>
    <Priority>4</Priority>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>cmd</Command>
      <Arguments>/c {{.XMLEscapedCommand}}</Arguments>
    </Exec>
  </Actions>
</Task>
'@
if (Test-Path variable:global:ProgressPreference){$ProgressPreference="SilentlyContinue"}
$f = $s.GetFolder("\")
$f.RegisterTaskDefinition($name, $t, 6, "{{.User}}", "{{.Password}}", 1, $null) | Out-Null
$t = $f.GetTask("\$name")
$t.Run($null) | Out-Null
$timeout = 10
$sec = 0
while ((!($t.state -eq 4)) -and ($sec -lt $timeout)) {
  Start-Sleep -s 1
  $sec++
}
$line = 0
do {
  Start-Sleep -m 100
  if (Test-Path $log) {
    Get-Content $log | select -skip $line | ForEach {
      $line += 1
      Write-Output "$_"
    }
  }
} while (!($t.state -eq 3))
$result = $t.LastTaskResult
if (Test-Path $log) {
    Remove-Item $log -Force -ErrorAction SilentlyContinue | Out-Null
}
[System.Runtime.Interopservices.Marshal]::ReleaseComObject($s) | Out-Null
exit $result`))
