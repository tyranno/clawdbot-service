; OpenClaw Gateway Installer
; NSIS Script

!include "MUI2.nsh"
!include "nsDialogs.nsh"
!include "LogicLib.nsh"

; ===== Product Info =====
!define PRODUCT_NAME "OpenClaw Gateway"
!define PRODUCT_PUBLISHER "OpenClaw"
!define PRODUCT_EXE "clawdbot-service.exe"
!define SERVICE_NAME "OpenClawGateway"
!define WATCHDOG_TASK "OpenClawWatchdog"
!define UNINST_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\${PRODUCT_NAME}"

; ===== Build Settings =====
Name "${PRODUCT_NAME}"
OutFile "..\OpenClawGateway-Setup.exe"
InstallDir "$PROGRAMFILES\OpenClaw"
InstallDirRegKey HKLM "${UNINST_KEY}" "InstallLocation"
RequestExecutionLevel admin
Unicode true

; ===== MUI Settings =====
!define MUI_ICON "${NSISDIR}\Contrib\Graphics\Icons\modern-install.ico"
!define MUI_UNICON "${NSISDIR}\Contrib\Graphics\Icons\modern-uninstall.ico"
!define MUI_ABORTWARNING

; ===== Pages =====
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

; ===== Language =====
!insertmacro MUI_LANGUAGE "Korean"

; ===== Version Info =====
VIProductVersion "1.0.0.0"
VIAddVersionKey "ProductName" "${PRODUCT_NAME}"
VIAddVersionKey "CompanyName" "${PRODUCT_PUBLISHER}"
VIAddVersionKey "FileDescription" "${PRODUCT_NAME} Installer"
VIAddVersionKey "FileVersion" "1.0.0.0"

; ===== Install Section =====
Section "Install"
    SetOutPath "$INSTDIR"

    ; --- Stop existing service & watchdog ---
    DetailPrint "Stopping existing service..."
    ; Create maintenance flag to prevent watchdog restart
    FileOpen $0 "$INSTDIR\maintenance.flag" w
    FileWrite $0 "installer"
    FileClose $0

    nsExec::ExecToLog '"$INSTDIR\${PRODUCT_EXE}" stop'
    ; Wait for service to stop
    Sleep 3000

    ; Stop watchdog scheduled task
    DetailPrint "Stopping watchdog..."
    nsExec::ExecToLog 'schtasks /end /tn "${WATCHDOG_TASK}"'
    Sleep 1000

    ; --- Copy files ---
    DetailPrint "Installing files..."
    File "..\clawdbot-service.exe"
    File "..\watchdog-restart.ps1"

    ; Create tool subdirectory
    CreateDirectory "$INSTDIR\tool"
    SetOutPath "$INSTDIR\tool"
    File "..\tool\idle-detector\idle-detector.exe"
    File "..\cmd\ecount-test\ecount-test.exe"

    ; Back to install dir
    SetOutPath "$INSTDIR"

    ; --- Register Windows Service ---
    DetailPrint "Registering Windows service..."
    ; First try uninstall in case it already exists
    nsExec::ExecToLog '"$INSTDIR\${PRODUCT_EXE}" uninstall'
    Sleep 1000
    nsExec::ExecToLog '"$INSTDIR\${PRODUCT_EXE}" install'
    Pop $0
    ${If} $0 != "0"
        DetailPrint "Warning: Service registration returned $0"
    ${EndIf}

    ; --- Register Watchdog Scheduled Task ---
    DetailPrint "Registering watchdog scheduled task..."
    ; Update watchdog script path in ps1 (it uses $scriptDir so no change needed)
    nsExec::ExecToLog 'powershell -ExecutionPolicy Bypass -Command "\
        $$action = New-ScheduledTaskAction -Execute \"powershell.exe\" -Argument \"-WindowStyle Hidden -ExecutionPolicy Bypass -File \\\"$INSTDIR\watchdog-restart.ps1\\\"\"; \
        $$trigger = New-ScheduledTaskTrigger -AtStartup; \
        $$settings = New-ScheduledTaskSettingsSet -ExecutionTimeLimit 0 -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1); \
        $$principal = New-ScheduledTaskPrincipal -UserId \"SYSTEM\" -RunLevel Highest; \
        Register-ScheduledTask -TaskName \"${WATCHDOG_TASK}\" -Action $$action -Trigger $$trigger -Settings $$settings -Principal $$principal -Force"'

    ; --- Remove maintenance flag and start service ---
    Delete "$INSTDIR\maintenance.flag"
    DetailPrint "Starting service..."
    nsExec::ExecToLog '"$INSTDIR\${PRODUCT_EXE}" start'

    ; --- Start watchdog ---
    DetailPrint "Starting watchdog..."
    nsExec::ExecToLog 'schtasks /run /tn "${WATCHDOG_TASK}"'

    ; --- Create uninstaller ---
    WriteUninstaller "$INSTDIR\uninstall.exe"

    ; --- Registry for Add/Remove Programs ---
    WriteRegStr HKLM "${UNINST_KEY}" "DisplayName" "${PRODUCT_NAME}"
    WriteRegStr HKLM "${UNINST_KEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
    WriteRegStr HKLM "${UNINST_KEY}" "InstallLocation" "$INSTDIR"
    WriteRegStr HKLM "${UNINST_KEY}" "Publisher" "${PRODUCT_PUBLISHER}"
    WriteRegDWORD HKLM "${UNINST_KEY}" "NoModify" 1
    WriteRegDWORD HKLM "${UNINST_KEY}" "NoRepair" 1

    DetailPrint "Installation complete!"
SectionEnd

; ===== Uninstall Section =====
Section "Uninstall"
    ; --- Create maintenance flag to prevent watchdog restart ---
    FileOpen $0 "$INSTDIR\maintenance.flag" w
    FileWrite $0 "uninstaller"
    FileClose $0

    ; --- Stop and remove service ---
    DetailPrint "Stopping service..."
    nsExec::ExecToLog '"$INSTDIR\${PRODUCT_EXE}" stop'
    Sleep 3000

    DetailPrint "Removing service..."
    nsExec::ExecToLog '"$INSTDIR\${PRODUCT_EXE}" uninstall'
    Sleep 1000

    ; --- Stop and remove watchdog ---
    DetailPrint "Stopping watchdog..."
    nsExec::ExecToLog 'schtasks /end /tn "${WATCHDOG_TASK}"'
    Sleep 1000

    DetailPrint "Removing watchdog scheduled task..."
    nsExec::ExecToLog 'powershell -ExecutionPolicy Bypass -Command "Unregister-ScheduledTask -TaskName \"${WATCHDOG_TASK}\" -Confirm:$$false -ErrorAction SilentlyContinue"'

    ; --- Delete files ---
    DetailPrint "Removing files..."
    Delete "$INSTDIR\clawdbot-service.exe"
    Delete "$INSTDIR\watchdog-restart.ps1"
    Delete "$INSTDIR\maintenance.flag"
    Delete "$INSTDIR\restart.trigger"
    Delete "$INSTDIR\uninstall.exe"
    Delete "$INSTDIR\tool\idle-detector.exe"
    Delete "$INSTDIR\tool\ecount-test.exe"
    RMDir "$INSTDIR\tool"
    RMDir "$INSTDIR"

    ; --- Remove registry ---
    DeleteRegKey HKLM "${UNINST_KEY}"

    DetailPrint "Uninstall complete!"
SectionEnd
