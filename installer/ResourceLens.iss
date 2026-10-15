#define AppName "拾流 ResourceLens"
#ifndef AppVersion
  #define AppVersion "1.6.0"
#endif
#ifndef ReleaseOutput
  #define ReleaseOutput "..\release"
#endif

[Setup]
AppId={{9DA5F698-13BD-47E4-A0E7-6B1A4DDD2764}
AppName={#AppName}
AppVersion={#AppVersion}
AppPublisher=ResourceLens
AppVerName={#AppName} {#AppVersion}
DefaultDirName={code:DefaultInstallDirectory}
DefaultGroupName={#AppName}
PrivilegesRequired=lowest
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
MinVersion=10.0
DisableProgramGroupPage=yes
DisableDirPage=no
DisableWelcomePage=no
WizardStyle=modern
SetupIconFile=ResourceLens.ico
UninstallDisplayIcon={app}\ResourceLens.exe
UninstallDisplayName={#AppName}
OutputDir={#ReleaseOutput}
OutputBaseFilename=ResourceLens-{#AppVersion}-Setup
VersionInfoVersion={#AppVersion}.0
VersionInfoProductName={#AppName}
VersionInfoDescription=拾流安装程序
Compression=lzma2
SolidCompression=yes
CloseApplications=yes
RestartApplications=no
SetupLogging=yes
AllowNetworkDrive=no
UsePreviousAppDir=yes

[Languages]
Name: "chinesesimp"; MessagesFile: "compiler:Default.isl"

[LangOptions]
LanguageName=简体中文
LanguageID=$0804
LanguageCodePage=0
DialogFontName=Microsoft YaHei UI
DialogFontSize=9
WelcomeFontName=Microsoft YaHei UI

[Messages]
SetupAppTitle=安装
SetupWindowTitle=安装 - %1
UninstallAppTitle=卸载
UninstallAppFullTitle=%1 卸载
InformationTitle=提示
ConfirmTitle=确认
ErrorTitle=出错了
SetupLdrStartupMessage=将开始安装 %1，是否继续？
SetupAlreadyRunning=安装程序已经在运行。
WindowsVersionNotSupported=此程序需要 Windows 10 或 Windows 11。
NotOnThisPlatform=此程序不支持 %1。
OnlyOnTheseArchitectures=此程序需要兼容 64 位应用的 Windows：%n%n%1
AdminPrivilegesRequired=当前安装位置需要管理员权限，请选择有写入权限的文件夹。
SetupAppRunningError=%1 正在运行。%n%n请先关闭程序，再点击“确定”继续安装。
UninstallAppRunningError=%1 正在运行。%n%n请先关闭程序，再继续卸载。
ErrorCreatingDir=无法创建文件夹“%1”。
ExitSetupTitle=退出安装
ExitSetupMessage=安装还没有完成。%n%n确定要退出吗？
AboutSetupMenuItem=关于安装程序
ButtonBack=< 上一步
ButtonNext=下一步 >
ButtonInstall=安装
ButtonOK=确定
ButtonCancel=取消
ButtonYes=是
ButtonYesToAll=全部是
ButtonNo=否
ButtonNoToAll=全部否
ButtonFinish=完成
ButtonBrowse=浏览...
ButtonWizardBrowse=浏览...
ButtonNewFolder=新建文件夹
SelectLanguageTitle=安装语言
SelectLanguageLabel=请选择安装语言。
ClickNext=点击“下一步”继续，或点击“取消”退出。
BrowseDialogTitle=选择文件夹
BrowseDialogLabel=选择安装文件夹，然后点击“确定”。
NewFolderName=新建文件夹
WelcomeLabel1=欢迎安装 [name]
WelcomeLabel2=将安装 [name/ver] 并创建桌面快捷方式。%n%n包含所需下载引擎；缺少 WebView2 时自动联网安装。%n%n点击“下一步”开始。
WizardSelectDir=选择安装位置
SelectDirDesc=将 [name] 安装到哪个文件夹？
SelectDirLabel3=程序将安装到下面的文件夹中。
SelectDirBrowseLabel=点击“下一步”使用此位置，或点击“浏览”选择其他文件夹。
DiskSpaceGBLabel=至少需要 [gb] GB 可用空间。
DiskSpaceMBLabel=至少需要 [mb] MB 可用空间。
CannotInstallToNetworkDrive=请选择本机磁盘上的文件夹。
CannotInstallToUNCPath=请选择本机磁盘上的文件夹。
InvalidPath=请输入完整安装路径，例如 D:\ResourceLens。
InvalidDrive=选定磁盘不存在或无法访问，请选择其他位置。
DiskSpaceWarningTitle=磁盘空间不足
DiskSpaceWarning=安装需要 %1 KB，当前只有 %2 KB 可用。%n%n是否继续？
DirNameTooLong=安装路径太长，请选择更短的路径。
InvalidDirName=安装文件夹名称无效。
DirExistsTitle=文件夹已存在
DirExists=文件夹已经存在：%n%n%1%n%n是否安装到此位置？
DirDoesntExistTitle=创建文件夹
DirDoesntExist=文件夹不存在：%n%n%1%n%n是否创建？
WizardSelectProgramGroup=开始菜单
SelectStartMenuFolderDesc=选择开始菜单文件夹。
SelectStartMenuFolderLabel3=程序快捷方式将放在以下文件夹中。
SelectStartMenuFolderBrowseLabel=点击“下一步”继续。
WizardReady=准备安装
ReadyLabel1=安装程序已准备好，可以开始安装 [name]。
ReadyLabel2a=点击“安装”继续，或点击“上一步”调整设置。
ReadyLabel2b=点击“安装”开始。
ReadyMemoDir=安装位置：
ReadyMemoGroup=开始菜单：
ReadyMemoTasks=附加任务：
WizardPreparing=正在准备安装
PreparingDesc=正在检查安装所需组件。
CannotContinue=暂时无法继续安装，请点击“取消”。
ApplicationsFound=以下程序正在使用需要更新的文件。请选择是否关闭它们。
ApplicationsFound2=以下程序正在使用需要更新的文件。请选择是否关闭它们。
CloseApplications=自动关闭相关程序
DontCloseApplications=保持相关程序运行
ErrorCloseApplications=部分程序无法关闭，请手动关闭后重试。
WizardInstalling=正在安装
InstallingLabel=正在安装 [name]，请稍候。
StatusExtractFiles=正在安装文件...
StatusCreateDirs=正在创建文件夹...
StatusCreateIcons=正在创建快捷方式...
StatusCreateIniEntries=正在写入设置...
StatusCreateRegistryEntries=正在登记卸载信息...
StatusRunProgram=正在完成安装...
StatusSavingUninstall=正在保存卸载信息...
FinishedHeadingLabel=安装完成
FinishedLabelNoIcons=[name] 已安装完成。
FinishedLabel=[name] 已安装完成，桌面快捷方式已创建。%n%n点击“完成”退出安装向导。
ClickFinish=点击“完成”退出。
FinishedRestartLabel=需要重启计算机才能完成安装，是否现在重启？
FinishedRestartMessage=需要重启计算机才能完成安装，是否现在重启？
YesRadio=立即重启
NoRadio=稍后重启
RunEntryExec=启动 %1
UninstallNotFound=没有找到卸载记录。
ConfirmUninstall=确定卸载 %1 吗？%n%n已下载文件及本机使用记录会保留。
UninstallStatusLabel=正在卸载 %1...
UninstalledAll=%1 已卸载。
UninstalledMost=%1 已卸载。使用过程中产生的文件已保留。

[Files]
Source: "..\ResourceLens.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\video-engine\*"; DestDir: "{app}\video-engine"; Flags: ignoreversion recursesubdirs createallsubdirs
Source: "..\README.md"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\LICENSE"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\启动拾流.cmd"; DestDir: "{app}"; Flags: ignoreversion
Source: "deps\MicrosoftEdgeWebview2Setup.exe"; Flags: dontcopy

[Icons]
Name: "{userdesktop}\{#AppName}"; Filename: "{app}\ResourceLens.exe"; WorkingDir: "{app}"; IconFilename: "{app}\ResourceLens.exe"; Comment: "捕获并下载视频、图片和音频"; AppUserModelID: "ResourceLens.Desktop"
Name: "{group}\{#AppName}"; Filename: "{app}\ResourceLens.exe"; WorkingDir: "{app}"; IconFilename: "{app}\ResourceLens.exe"; AppUserModelID: "ResourceLens.Desktop"
Name: "{group}\卸载拾流"; Filename: "{uninstallexe}"

[Run]
Filename: "{app}\ResourceLens.exe"; Description: "启动拾流"; Flags: nowait postinstall skipifsilent unchecked

[Code]
function DefaultInstallDirectory(Param: String): String;
begin
  if DirExists('D:\') then
    Result := 'D:\ResourceLens'
  else
    Result := ExpandConstant('{localappdata}\Programs\ResourceLens');
end;

function RegistryRuntimePresent(RootKey: Integer): Boolean;
var
  Version: String;
begin
  Result := RegQueryStringValue(RootKey,
    'SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}',
    'pv', Version) and (Version <> '') and (Version <> '0.0.0.0');
end;

function WebView2Installed: Boolean;
begin
  Result := RegistryRuntimePresent(HKCU32) or RegistryRuntimePresent(HKLM32);
  if IsWin64 and not Result then
    Result := RegistryRuntimePresent(HKCU64) or RegistryRuntimePresent(HKLM64);
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  ExitCode, Attempt: Integer;
begin
  Result := '';
  if WebView2Installed then begin
    Log('WebView2 is already present; dependency installation skipped.');
    Exit;
  end;
  WizardForm.PreparingLabel.Caption := '正在联网安装 Microsoft WebView2，请稍候...';
  ExtractTemporaryFile('MicrosoftEdgeWebview2Setup.exe');
  if not Exec(ExpandConstant('{tmp}\MicrosoftEdgeWebview2Setup.exe'),
    '/silent /install', '', SW_HIDE, ewWaitUntilTerminated, ExitCode) then begin
    Result := '无法启动 WebView2 安装程序，请重试。';
    Exit;
  end;
  for Attempt := 1 to 10 do begin
    if WebView2Installed then begin
      NeedsRestart := (ExitCode = 3010) or (ExitCode = 1641);
      Exit;
    end;
    Sleep(1000);
  end;
  Result := 'WebView2 未安装完成，请检查网络连接后重试。安装返回代码：' + IntToStr(ExitCode);
end;
