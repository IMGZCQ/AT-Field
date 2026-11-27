@echo off
setlocal enabledelayedexpansion
title AT-ForceField Build

cd /d "%~dp0"
set "SRC=code"
set "OUT=build"
set "NAME=at-forcefield"
set "APPNAME=AT-ForceField"
rem FPKB64 = base64(UTF-8) of the Chinese app name (version suffix is appended at pack time)
rem (kept in base64 because cmd parses this .bat with the OEM code page)
set "FPKB64=6aOe54mbQVTlipvlnLo="
set "CGO_ENABLED=0"
if "%GOPROXY%"=="" set "GOPROXY=https://goproxy.cn,direct"

where go >nul 2>nul
if errorlevel 1 goto no_go

rem Read version from fpk\manifest
set "VERSION=1.0.0"
for /f "usebackq tokens=2 delims==" %%a in (`findstr /b /c:"version=" "fpk\manifest"`) do set "VERSION=%%a"
echo [Info] Version: %VERSION%

rem ========== 1. Frontend ==========
where npm >nul 2>nul
if errorlevel 1 goto no_npm
if not exist "%SRC%\frontend\node_modules" (
    echo [Frontend] Installing dependencies...
    pushd %SRC%\frontend
    call npm install
    set "RC=!ERRORLEVEL!"
    popd
    if not "!RC!"=="0" goto fail
)
echo [Frontend] Building dist...
pushd %SRC%\frontend
set "VITE_APP_VERSION=%VERSION%"
call npm run build
set "RC=!ERRORLEVEL!"
popd
if not "!RC!"=="0" goto fail

rem ========== 2. Go build (Linux amd64 + arm64) ==========
if not exist "%OUT%" mkdir "%OUT%" >nul
call :go_build amd64
if errorlevel 1 goto fail
call :go_build arm64
if errorlevel 1 goto fail

rem ========== 3. Assemble FPK staging ==========
echo [FPK] Assembling staging...
set "STAGE=%OUT%\fpk-stage"
set "APPDIR=%STAGE%\app"
if exist "%STAGE%" rmdir /s /q "%STAGE%"
mkdir "%APPDIR%\ui\images" >nul 2>nul
mkdir "%APPDIR%\config" >nul 2>nul

rem app/ payload: binaries + ui + config
copy /y "%OUT%\%NAME%-linux-amd64" "%APPDIR%\%NAME%-linux-amd64" >nul
copy /y "%OUT%\%NAME%-linux-arm64" "%APPDIR%\%NAME%-linux-arm64" >nul
copy /y "fpk\app\ui\config" "%APPDIR%\ui\config" >nul
copy /y "fpk\app\ui\images\icon_256.png" "%APPDIR%\ui\images\icon_256.png" >nul
copy /y "fpk\app\ui\images\icon_64.png" "%APPDIR%\ui\images\icon_64.png" >nul
copy /y "fpk\app\config\privilege" "%APPDIR%\config\privilege" >nul
copy /y "fpk\app\config\resource" "%APPDIR%\config\resource" >nul

rem fpk root: app.tgz + manifest + icons + cmd + config + wizard
pushd "%APPDIR%"
tar -czf "..\app.tgz" *
popd
copy /y "fpk\manifest" "%STAGE%\manifest" >nul
copy /y "fpk\ICON.PNG" "%STAGE%\ICON.PNG" >nul
copy /y "fpk\ICON_256.PNG" "%STAGE%\ICON_256.PNG" >nul
xcopy /e /y /i "fpk\cmd" "%STAGE%\cmd" >nul
xcopy /e /y /i "fpk\config" "%STAGE%\config" >nul
xcopy /e /y /i "fpk\wizard" "%STAGE%\wizard" >nul

rem ========== 4. Pack .fpk ==========
rem Pack to an ASCII name first, then output two copies via PowerShell:
rem   1) ChineseName_version.fpk  (release artifact)
rem   2) AT-ForceField.fpk        (pure ASCII, no version, for scripts / mirror uploads)
rem (cmd would mis-parse this file if it contained non-ASCII literals)
set "FPK=%APPNAME%_%VERSION%.fpk"
set "FPK_EN=%APPNAME%.fpk"
echo [FPK] Packing %FPK%...
pushd "%STAGE%"
tar -czf "..\..\%FPK%" app.tgz manifest ICON.PNG ICON_256.PNG cmd config wizard
set "RC=!ERRORLEVEL!"
popd
if not "!RC!"=="0" goto fail

for %%f in ("%OUT%\%NAME%-linux-*") do echo        %%~nxf  %%~zf bytes

powershell -NoProfile -ExecutionPolicy Bypass -Command "$b=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('%FPKB64%')); $n=$b+'_'+'%VERSION%'+'.fpk'; Move-Item -Force -LiteralPath '%FPK%' -Destination $n; Copy-Item -Force -LiteralPath $n -Destination '%FPK_EN%'; $f=Get-Item -LiteralPath $n; $e=Get-Item -LiteralPath '%FPK_EN%'; Write-Host ''; Write-Host ('[Success] Output: '+$f.FullName); Write-Host ('       '+$f.Name+'  '+$f.Length+' bytes'); Write-Host ('       '+$e.Name+'  '+$e.Length+' bytes')"
set "RC=!ERRORLEVEL!"
if not "!RC!"=="0" goto fail
goto done

:go_build
setlocal
set "ARCH=%~1"
echo [Build] linux/%ARCH%
pushd %SRC%
set GOOS=linux
set GOARCH=%ARCH%
go build -trimpath -ldflags "-s -w -X main.Version=%VERSION%" -o "..\%OUT%\%NAME%-linux-%ARCH%" ./cmd/server
set "RC=%ERRORLEVEL%"
popd
if not "%RC%"=="0" (
    echo [Failed] linux/%ARCH% build failed
    exit /b 1
)
echo        Output: %OUT%\%NAME%-linux-%ARCH%
endlocal
exit /b 0

:no_npm
echo [Error] npm not found, cannot build frontend
exit /b 1

:no_go
echo [Error] Go not found, please install Go and add to PATH
exit /b 1

:fail
echo.
echo [Interrupted] Build failed, check errors above
if /i not "%~1"=="nopause" pause
exit /b 1

:done
endlocal
if /i not "%~1"=="nopause" pause
exit /b 0
