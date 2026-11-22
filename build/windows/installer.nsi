; The Windows installer: makensis -DVERSION=2.1.0 -DNUMERIC=2.1.0.0 -DSOURCE=dist\windows -DOUTFILE=... installer.nsi
; Made by scripts/build-windows.sh.
;
; It installs for the person running it (no administrator needed) into
; %LOCALAPPDATA%\Programs\Banyan. The register is not in there: it lives in
; %APPDATA%\Banyan, and neither installing nor uninstalling ever touches it.

Unicode true
SetCompressor /SOLID lzma
RequestExecutionLevel user

!include "MUI2.nsh"

Name "Banyan"
OutFile "${OUTFILE}"
InstallDir "$LOCALAPPDATA\Programs\Banyan"
InstallDirRegKey HKCU "Software\Banyan" "InstallDir"
BrandingText "Banyan ${VERSION}"

VIProductVersion "${NUMERIC}"
VIAddVersionKey "ProductName" "Banyan"
VIAddVersionKey "FileDescription" "Banyan installer"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "FileVersion" "${VERSION}"

!define MUI_ICON "icon.ico"
!define MUI_UNICON "icon.ico"
!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_RUN "$INSTDIR\Banyan.exe"
!define MUI_FINISHPAGE_RUN_TEXT "Open Banyan"

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"

!define UNINSTALL_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\Banyan"

Section "Banyan"
  SetOutPath "$INSTDIR"
  ; A running Banyan holds Banyan.exe open; say so rather than half-install.
  retry:
  IfFileExists "$INSTDIR\Banyan.exe" 0 install
  ClearErrors
  FileOpen $0 "$INSTDIR\Banyan.exe" a
  IfErrors 0 opened
  MessageBox MB_RETRYCANCEL|MB_ICONEXCLAMATION "Banyan is open. Quit it (File > Exit), then choose Retry." IDRETRY retry
  Abort
  opened:
  FileClose $0
  install:
  File "${SOURCE}\Banyan.exe"
  File "${SOURCE}\pdfium.dll"
  SetOutPath "$INSTDIR\licenses"
  File /r "${SOURCE}\licenses\*"
  SetOutPath "$INSTDIR"

  CreateShortcut "$SMPROGRAMS\Banyan.lnk" "$INSTDIR\Banyan.exe"
  CreateShortcut "$DESKTOP\Banyan.lnk" "$INSTDIR\Banyan.exe"

  WriteUninstaller "$INSTDIR\Uninstall Banyan.exe"
  WriteRegStr HKCU "Software\Banyan" "InstallDir" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayName" "Banyan"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "Publisher" "Banyan"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayIcon" "$INSTDIR\Banyan.exe"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "UninstallString" '"$INSTDIR\Uninstall Banyan.exe"'
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "NoRepair" 1
SectionEnd

; Removes the app only. The register, calibration and rendered cheques in
; %APPDATA%\Banyan stay where they are.
Section "Uninstall"
  Delete "$INSTDIR\Banyan.exe"
  Delete "$INSTDIR\pdfium.dll"
  RMDir /r "$INSTDIR\licenses"
  Delete "$INSTDIR\Uninstall Banyan.exe"
  RMDir "$INSTDIR"
  Delete "$SMPROGRAMS\Banyan.lnk"
  Delete "$DESKTOP\Banyan.lnk"
  DeleteRegKey HKCU "${UNINSTALL_KEY}"
  DeleteRegKey HKCU "Software\Banyan"
SectionEnd
