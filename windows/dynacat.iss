#ifndef AppVersion
  #define AppVersion "dev"
#endif
#ifndef Arch
  #define Arch "amd64"
#endif
#if Arch == "arm64"
  #define InnoArch "arm64"
#else
  #define InnoArch "x64compatible"
#endif

[Setup]
AppId={{32A3A908-8CA9-41A6-820A-DFDCCE1C16D1}
AppName=Dynacat
AppVersion={#AppVersion}
AppPublisher=Panonim
AppPublisherURL=https://github.com/Panonim/dynacat
DefaultDirName={localappdata}\Dynacat
DisableProgramGroupPage=yes
PrivilegesRequired=admin
UsedUserAreasWarning=no
ArchitecturesAllowed={#InnoArch}
ArchitecturesInstallIn64BitMode={#InnoArch}
OutputDir=output
OutputBaseFilename=dynacat-windows-{#Arch}-setup
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
SetupIconFile=setup.ico
UninstallDisplayIcon={app}\dynacat.exe

[Tasks]
Name: desktopicon; Description: "Create a desktop shortcut"; GroupDescription: "Shortcuts"; Flags: unchecked
Name: background; Description: "Run in the background without a window (logs go to Event Viewer)"; GroupDescription: "Startup"; Flags: unchecked
Name: autostart; Description: "Start Dynacat in the background when I log in"; GroupDescription: "Startup"; Flags: unchecked
Name: firewall; Description: "Allow access from other devices on the network (Windows Firewall rule)"; GroupDescription: "Network"; Flags: unchecked

[Files]
Source: "dynacat.exe"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{userprograms}\Dynacat"; Filename: "{app}\dynacat.exe"; Parameters: "{code:GetRunParams|console}"; WorkingDir: "{app}"; Tasks: not background
Name: "{userprograms}\Dynacat"; Filename: "{app}\dynacat.exe"; Parameters: "{code:GetRunParams|background}"; WorkingDir: "{app}"; Flags: runminimized; Tasks: background
Name: "{userprograms}\Update Dynacat"; Filename: "{app}\dynacat.exe"; Parameters: "update"; WorkingDir: "{app}"
Name: "{userprograms}\Stop Dynacat"; Filename: "{sys}\taskkill.exe"; Parameters: "/F /IM dynacat.exe"; IconFilename: "{sys}\shell32.dll"; IconIndex: 27; Flags: runminimized; Tasks: background or autostart
Name: "{userdesktop}\Dynacat"; Filename: "{app}\dynacat.exe"; Parameters: "{code:GetRunParams|console}"; WorkingDir: "{app}"; Tasks: desktopicon and not background
Name: "{userdesktop}\Dynacat"; Filename: "{app}\dynacat.exe"; Parameters: "{code:GetRunParams|background}"; WorkingDir: "{app}"; Flags: runminimized; Tasks: desktopicon and background
Name: "{userstartup}\Dynacat"; Filename: "{app}\dynacat.exe"; Parameters: "{code:GetRunParams|background}"; WorkingDir: "{app}"; Flags: runminimized; Tasks: autostart

[Registry]
Root: HKLM; Subkey: "SYSTEM\CurrentControlSet\Services\EventLog\Application\Dynacat"; ValueType: expandsz; ValueName: "EventMessageFile"; ValueData: "%SystemRoot%\System32\EventCreate.exe"; Flags: uninsdeletekey
Root: HKLM; Subkey: "SYSTEM\CurrentControlSet\Services\EventLog\Application\Dynacat"; ValueType: dword; ValueName: "TypesSupported"; ValueData: "7"
Root: HKCU; Subkey: "Software\Dynacat"; ValueType: string; ValueName: "EnvFile"; ValueData: "{code:GetEnvFile}"; Flags: uninsdeletekey

[Run]
Filename: "{sys}\netsh.exe"; Parameters: "advfirewall firewall delete rule name=""Dynacat"""; Flags: runhidden
Filename: "{sys}\netsh.exe"; Parameters: "advfirewall firewall add rule name=""Dynacat"" dir=in action=allow protocol=TCP localport={code:GetPort}"; Flags: runhidden; Tasks: firewall
Filename: "{app}\dynacat.exe"; Parameters: "{code:GetRunParams|console}"; WorkingDir: "{app}"; Description: "Launch Dynacat"; Flags: postinstall nowait skipifsilent; Tasks: not background
Filename: "{app}\dynacat.exe"; Parameters: "{code:GetRunParams|background}"; WorkingDir: "{app}"; Description: "Launch Dynacat"; Flags: postinstall nowait skipifsilent runhidden; Tasks: background
Filename: "http://localhost:{code:GetPort}"; Description: "Open Dynacat in the browser"; Flags: postinstall shellexec nowait skipifsilent
Filename: "{app}\dynacat.exe"; Parameters: "{code:GetRunParams|console}"; WorkingDir: "{app}"; Flags: nowait runasoriginaluser; Tasks: not background; Check: IsRelaunch
Filename: "{app}\dynacat.exe"; Parameters: "{code:GetRunParams|background}"; WorkingDir: "{app}"; Flags: nowait runasoriginaluser runhidden; Tasks: background; Check: IsRelaunch

[UninstallRun]
Filename: "{sys}\taskkill.exe"; Parameters: "/F /IM dynacat.exe"; Flags: runhidden; RunOnceId: "StopDynacat"
Filename: "{sys}\netsh.exe"; Parameters: "advfirewall firewall delete rule name=""Dynacat"""; Flags: runhidden; RunOnceId: "DeleteFirewallRule"

[UninstallDelete]
Type: filesandordirs; Name: "{app}\.cache"
Type: filesandordirs; Name: "{app}\assets\dynawidgets"

[Code]
const
  DefaultPort = '8080';

var
  EnvCheck: TNewCheckBox;
  EnvEdit: TNewEdit;
  EnvBrowseButton: TNewButton;
  PortCheck: TNewCheckBox;
  PortEdit: TNewEdit;
  PortNote: TNewStaticText;

function ConfigPath(): String;
begin
  Result := AddBackslash(WizardDirValue) + 'config\dynacat.yml';
end;

function IsUpgrade(): Boolean;
begin
  Result := FileExists(ConfigPath());
end;

function GetEnvDir(Param: String): String;
begin
  Result := '';
  if EnvCheck.Checked then
    Result := Trim(EnvEdit.Text);
  if Result = '' then
    Result := WizardDirValue;
end;

function GetPort(Param: String): String;
begin
  Result := DefaultPort;
  if PortCheck.Checked then
    Result := Trim(PortEdit.Text);
end;

function GetEnvFile(Param: String): String;
begin
  Result := AddBackslash(GetEnvDir('')) + '.env';
end;

// Set by `dynacat update`, which runs this installer silently and expects Dynacat to start again.
function IsRelaunch(): Boolean;
var
  I: Integer;
begin
  Result := False;
  for I := 1 to ParamCount do
    if CompareText(ParamStr(I), '/RELAUNCH') = 0 then
      Result := True;
end;

function GetRunParams(Mode: String): String;
begin
  Result := Format('--config "%s" --env-file "%s"', [ConfigPath(), GetEnvFile('')]);
  if Mode = 'background' then
    Result := Result + ' --background';
end;

procedure EnvCheckClick(Sender: TObject);
begin
  EnvEdit.Enabled := EnvCheck.Checked;
  EnvBrowseButton.Enabled := EnvCheck.Checked;
  if EnvCheck.Checked and (Trim(EnvEdit.Text) = '') then
    EnvEdit.Text := WizardDirValue;
end;

procedure EnvBrowseButtonClick(Sender: TObject);
var
  Dir: String;
begin
  Dir := EnvEdit.Text;
  if BrowseForFolder('Select the folder for the .env file:', Dir, True) then
    EnvEdit.Text := Dir;
end;

procedure PortCheckClick(Sender: TObject);
begin
  PortEdit.Enabled := PortCheck.Checked;
end;

// Adds the .env folder option under the install folder picker.
procedure CreateEnvControls();
var
  Page: TWizardPage;
  Hint: TNewStaticText;
  PrevEnvDir: String;
begin
  Page := PageFromID(wpSelectDir);

  EnvCheck := TNewCheckBox.Create(WizardForm);
  EnvCheck.Parent := Page.Surface;
  EnvCheck.Top := WizardForm.DirEdit.Top + WizardForm.DirEdit.Height + ScaleY(24);
  EnvCheck.Width := Page.SurfaceWidth;
  EnvCheck.Height := ScaleY(17);
  EnvCheck.Caption := 'Store the .env file in a different folder';
  EnvCheck.OnClick := @EnvCheckClick;

  EnvEdit := TNewEdit.Create(WizardForm);
  EnvEdit.Parent := Page.Surface;
  EnvEdit.Top := EnvCheck.Top + EnvCheck.Height + ScaleY(8);
  EnvEdit.Left := WizardForm.DirEdit.Left;
  EnvEdit.Width := WizardForm.DirEdit.Width;

  EnvBrowseButton := TNewButton.Create(WizardForm);
  EnvBrowseButton.Parent := Page.Surface;
  EnvBrowseButton.Top := EnvEdit.Top + (EnvEdit.Height - WizardForm.DirBrowseButton.Height) div 2;
  EnvBrowseButton.Left := WizardForm.DirBrowseButton.Left;
  EnvBrowseButton.Width := WizardForm.DirBrowseButton.Width;
  EnvBrowseButton.Height := WizardForm.DirBrowseButton.Height;
  EnvBrowseButton.Caption := WizardForm.DirBrowseButton.Caption;
  EnvBrowseButton.OnClick := @EnvBrowseButtonClick;

  Hint := TNewStaticText.Create(WizardForm);
  Hint.Parent := Page.Surface;
  Hint.Top := EnvEdit.Top + EnvEdit.Height + ScaleY(6);
  Hint.Width := Page.SurfaceWidth;
  Hint.AutoSize := True;
  Hint.WordWrap := True;
  Hint.Caption := 'The .env file holds variables you can use anywhere in the config with ${NAME}. By default it is stored in the install folder.';

  PrevEnvDir := GetPreviousData('EnvDir', '');
  EnvCheck.Checked := PrevEnvDir <> '';
  EnvEdit.Text := PrevEnvDir;
  EnvCheckClick(nil);
end;

// Adds the custom port option below the task list.
procedure CreatePortControls();
var
  Page: TWizardPage;
  PrevPort: String;
begin
  Page := PageFromID(wpSelectTasks);
  WizardForm.TasksList.Height := WizardForm.TasksList.Height - ScaleY(40);

  PortCheck := TNewCheckBox.Create(WizardForm);
  PortCheck.Parent := Page.Surface;
  PortCheck.Top := WizardForm.TasksList.Top + WizardForm.TasksList.Height + ScaleY(16);
  PortCheck.Left := WizardForm.TasksList.Left;
  PortCheck.Width := ScaleX(200);
  PortCheck.Height := ScaleY(17);
  PortCheck.Caption := 'Use a custom port instead of ' + DefaultPort;
  PortCheck.OnClick := @PortCheckClick;

  PortEdit := TNewEdit.Create(WizardForm);
  PortEdit.Parent := Page.Surface;
  PortEdit.Left := PortCheck.Left + PortCheck.Width + ScaleX(8);
  PortEdit.Width := ScaleX(70);
  PortEdit.Top := PortCheck.Top + (PortCheck.Height - PortEdit.Height) div 2;
  PortEdit.MaxLength := 5;

  PortNote := TNewStaticText.Create(WizardForm);
  PortNote.Parent := Page.Surface;
  PortNote.Top := PortCheck.Top;
  PortNote.Left := PortCheck.Left;
  PortNote.Caption := 'The port is kept from your existing config.';

  PrevPort := GetPreviousData('Port', DefaultPort);
  PortCheck.Checked := PrevPort <> DefaultPort;
  PortEdit.Text := PrevPort;
  PortCheckClick(nil);
end;

function RunListIndex(Caption: String): Integer;
var
  I: Integer;
begin
  Result := -1;
  for I := 0 to WizardForm.RunList.Items.Count - 1 do
    if WizardForm.RunList.ItemCaption[I] = Caption then
      Result := I;
end;

// Opening the browser is only useful when Dynacat is launched too.
procedure RunListClickCheck(Sender: TObject);
var
  LaunchIndex, BrowserIndex: Integer;
begin
  LaunchIndex := RunListIndex('Launch Dynacat');
  BrowserIndex := RunListIndex('Open Dynacat in the browser');
  if (LaunchIndex < 0) or (BrowserIndex < 0) then
    Exit;

  WizardForm.RunList.ItemEnabled[BrowserIndex] := WizardForm.RunList.Checked[LaunchIndex];
  WizardForm.RunList.Checked[BrowserIndex] := WizardForm.RunList.Checked[LaunchIndex];
end;

procedure InitializeWizard;
begin
  CreateEnvControls();
  CreatePortControls();
  WizardForm.RunList.OnClickCheck := @RunListClickCheck;
end;

procedure RegisterPreviousData(PreviousDataKey: Integer);
var
  EnvDir: String;
begin
  EnvDir := '';
  if EnvCheck.Checked then
    EnvDir := GetEnvDir('');
  SetPreviousData(PreviousDataKey, 'EnvDir', EnvDir);
  SetPreviousData(PreviousDataKey, 'Port', GetPort(''));
end;

// Upgrades keep the existing config, so its port can not be changed here.
procedure CurPageChanged(CurPageID: Integer);
begin
  if CurPageID = wpSelectTasks then
  begin
    PortCheck.Visible := not IsUpgrade();
    PortEdit.Visible := not IsUpgrade();
    PortNote.Visible := IsUpgrade();
  end;
end;

function NextButtonClick(CurPageID: Integer): Boolean;
var
  Port: Integer;
begin
  Result := True;

  if (CurPageID = wpSelectDir) and EnvCheck.Checked and (Trim(EnvEdit.Text) = '') then
  begin
    MsgBox('Choose a folder for the .env file or uncheck the option.', mbError, MB_OK);
    Result := False;
  end;

  if (CurPageID = wpSelectTasks) and PortCheck.Visible then
  begin
    Port := StrToIntDef(GetPort(''), 0);
    if (Port < 1) or (Port > 65535) then
    begin
      MsgBox('Enter a port between 1 and 65535.', mbError, MB_OK);
      Result := False;
    end;
  end;
end;

function UpdateReadyMemo(Space, NewLine, MemoUserInfoInfo, MemoDirInfo, MemoTypeInfo,
  MemoComponentsInfo, MemoGroupInfo, MemoTasksInfo: String): String;
begin
  Result := MemoDirInfo + NewLine + NewLine +
    'Environment file:' + NewLine + Space + GetEnvFile('') + NewLine + NewLine +
    'Dynacat will be available at:' + NewLine + Space + 'http://localhost:' + GetPort('');
  if IsUpgrade() then
    Result := Result + NewLine + NewLine + 'Your existing config, pages and assets are kept.';
  if MemoTasksInfo <> '' then
    Result := Result + NewLine + NewLine + MemoTasksInfo;
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  ResultCode: Integer;
begin
  Exec(ExpandConstant('{sys}\taskkill.exe'), '/F /IM dynacat.exe', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
  Result := '';
end;

// The config has no pages, so Dynacat opens its first run setup page to create them.
procedure CurStepChanged(CurStep: TSetupStep);
var
  AssetsPath: String;
begin
  if CurStep <> ssPostInstall then
    Exit;

  AssetsPath := ExpandConstant('{app}\assets');
  ForceDirectories(AssetsPath);
  ForceDirectories(ExtractFileDir(ConfigPath()));
  ForceDirectories(GetEnvDir(''));

  if not FileExists(AssetsPath + '\user.css') then
    SaveStringToFile(AssetsPath + '\user.css', '', False);

  if not FileExists(ConfigPath()) then
  begin
    StringChangeEx(AssetsPath, '''', '''''', True);
    SaveStringToFile(ConfigPath(),
      'server:' + #13#10 +
      '  port: ' + GetPort('') + #13#10 +
      '  assets-path: ''' + AssetsPath + '''' + #13#10 + #13#10 +
      'theme:' + #13#10 +
      '  # Note: assets are cached by the browser, changes to the CSS file' + #13#10 +
      '  # will not be reflected until the browser cache is cleared (Ctrl+F5)' + #13#10 +
      '  custom-css-file: /assets/user.css' + #13#10, False);
  end;

  if not FileExists(GetEnvFile('')) then
    SaveStringToFile(GetEnvFile(''),
      '# Variables defined here will be available to use anywhere in the config with the syntax ${MY_SECRET_TOKEN}' + #13#10 +
      '# Note: making changes to this file requires restarting Dynacat' + #13#10, False);
end;

var
  RemoveUserData: Boolean;

// Runs after the built in Yes/No confirmation, sized and styled like that message box.
procedure InitializeUninstallProgressForm();
var
  Form: TSetupForm;
  Body: TPanel;
  RemoveDataCheck: TNewCheckBox;
  Hint: TNewStaticText;
  UninstallButton, CancelButton: TNewButton;
  ButtonWidth: Integer;
begin
  if UninstallSilent then
    Exit;

  Form := CreateCustomForm(ScaleX(440), ScaleY(130), False, True);
  try
    Form.Caption := 'Dynacat Uninstall';

    Body := TPanel.Create(Form);
    Body.Parent := Form;
    Body.Align := alTop;
    Body.Height := Form.ClientHeight - ScaleY(46);
    Body.BevelOuter := bvNone;
    Body.ParentBackground := False;
    Body.Color := clWindow;

    RemoveDataCheck := TNewCheckBox.Create(Form);
    RemoveDataCheck.Parent := Body;
    RemoveDataCheck.Left := ScaleX(24);
    RemoveDataCheck.Top := ScaleY(24);
    RemoveDataCheck.Width := Form.ClientWidth - RemoveDataCheck.Left - ScaleX(16);
    RemoveDataCheck.Height := ScaleY(17);
    RemoveDataCheck.Caption := 'Also remove my pages, settings and .env file';

    Hint := TNewStaticText.Create(Form);
    Hint.Parent := Body;
    // Matches where Windows draws the checkbox label, after the 13px box and a 3px gap.
    Hint.Left := RemoveDataCheck.Left + ScaleX(16);
    Hint.Top := RemoveDataCheck.Top + RemoveDataCheck.Height + ScaleY(4);
    Hint.Caption := 'Leave unchecked to keep them for a later install.';

    UninstallButton := TNewButton.Create(Form);
    UninstallButton.Parent := Form;
    UninstallButton.Caption := 'Uninstall';
    UninstallButton.ModalResult := mrOk;
    UninstallButton.Default := True;

    CancelButton := TNewButton.Create(Form);
    CancelButton.Parent := Form;
    CancelButton.Caption := 'Cancel';
    CancelButton.ModalResult := mrCancel;
    CancelButton.Cancel := True;

    ButtonWidth := Form.CalculateButtonWidth([UninstallButton.Caption, CancelButton.Caption]);
    UninstallButton.SetBounds(Form.ClientWidth - 2 * ButtonWidth - ScaleX(16 + 8), Form.ClientHeight - ScaleY(23 + 12), ButtonWidth, ScaleY(23));
    CancelButton.SetBounds(Form.ClientWidth - ButtonWidth - ScaleX(16), UninstallButton.Top, ButtonWidth, ScaleY(23));

    Form.ActiveControl := UninstallButton;

    if Form.ShowModal() <> mrOk then
      Abort;

    RemoveUserData := RemoveDataCheck.Checked;
  finally
    Form.Free;
  end;
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  EnvFile: String;
begin
  if CurUninstallStep = usUninstall then
  begin
    // Read before uninstall deletes the registry key.
    if RemoveUserData and RegQueryStringValue(HKCU, 'Software\Dynacat', 'EnvFile', EnvFile) then
      DeleteFile(EnvFile);
  end;

  if (CurUninstallStep = usPostUninstall) and RemoveUserData then
  begin
    DelTree(ExpandConstant('{app}\config'), True, True, True);
    DelTree(ExpandConstant('{app}\assets'), True, True, True);
    RemoveDir(ExpandConstant('{app}'));
  end;
end;
