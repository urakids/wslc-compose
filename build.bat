@echo off
SET VERSION=0.1.0

setlocal
cd /d "%~dp0"

where go >nul 2>&1
if errorlevel 1 (
    if exist "%ProgramFiles%\Go\bin\go.exe" (
        set "PATH=%ProgramFiles%\Go\bin;%PATH%"
    ) else (
        echo error: go not found. Install Go: winget install --id GoLang.Go -e --source winget
        exit /b 1
    )
)

echo Building wslc-compose.exe version %VERSION% ...
if not exist build mkdir build
go build -trimpath -ldflags "-s -w -X main.version=%VERSION%" -o build\wslc-compose.exe .
if errorlevel 1 (
    echo build failed.
    exit /b 1
)

.\build\wslc-compose.exe --version
endlocal
