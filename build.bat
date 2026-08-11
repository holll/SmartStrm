@echo off
setlocal enabledelayedexpansion

REM ======================================================
REM         SmartStrm-Go Build Script
REM   Multi-Platform (windows/amd64, linux/amd64)
REM   Versioning (Git Tag + Commit + Build Time)
REM ======================================================

REM Get version from git tag
for /f "delims=" %%a in ('git describe --tags --abbrev^=0 2^>nul') do set VERSION=%%a
if "%VERSION%"=="" set VERSION=0.0.0

REM Get short commit hash
for /f "delims=" %%a in ('git rev-parse --short HEAD') do set COMMIT=%%a

REM Get build timestamp
for /f "delims=" %%a in ('powershell -command "Get-Date -Format yyyy-MM-dd_HH-mm-ss"') do set BUILDTIME=%%a

echo Version: %VERSION%
echo Commit:  %COMMIT%
echo Time:    %BUILDTIME%
echo.

REM Output directory
set OUTDIR=dist
if not exist %OUTDIR% mkdir %OUTDIR%

REM Write version info
echo Version=%VERSION%> %OUTDIR%\version.txt
echo Commit=%COMMIT%>> %OUTDIR%\version.txt
echo BuildTime=%BUILDTIME%>> %OUTDIR%\version.txt

REM =======================
REM   Target platforms
REM =======================
set TARGETS=^
windows/amd64 ^
linux/amd64

REM ==========================
REM   Build loop
REM ==========================
for %%T in (%TARGETS%) do (
    for /f "tokens=1,2 delims=/" %%a in ("%%T") do (
        set GOOS=%%a
        set GOARCH=%%b
        set CGO_ENABLED=0

        set EXT=
        if "!GOOS!"=="windows" set EXT=.exe

        set OUTFILE=%OUTDIR%\smartstrm_!GOOS!_!GOARCH!!EXT!

        echo ---------------------------------------------------
        echo Building !OUTFILE!
        echo ---------------------------------------------------

        go build ^
            -o "!OUTFILE!" ^
            -ldflags "-s -w -X main.Version=%VERSION% -X main.Commit=%COMMIT% -X main.BuildTime=%BUILDTIME%" ^
            ./cmd/server

        if errorlevel 1 (
            echo.
            echo Build failed: !GOOS!/!GOARCH!
            pause
            exit /b 1
        )

        REM UPX compression (if available)
        where upx >nul 2>nul
        if !errorlevel!==0 (
            echo Compressing with UPX...
            upx --best --lzma "!OUTFILE!"
        ) else (
            echo UPX not found, skipping compression
        )
    )
)

REM 本地测试工具（mock OpenList，仅 Windows 本地使用）
echo ---------------------------------------------------
echo Building mock-openlist (local test tool)...
echo ---------------------------------------------------
go build -o build\mock-openlist.exe ./mock/openlist
if errorlevel 1 (
    echo Build failed: mock-openlist
    pause
    exit /b 1
)

echo.
echo ===========================================
echo All builds completed successfully!
echo Binaries in: %OUTDIR%
echo ===========================================
echo.

pause
