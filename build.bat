@echo off
SET VERSION=0.1.1

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

rem Generate Windows resources (icon + version info) as .syso; go build embeds it automatically
go run github.com/tc-hib/go-winres@v0.3.3 simply --arch amd64 --manifest cli --icon icon\icon.png ^
    --product-name wslc-compose --file-description "wslc-compose" ^
    --product-version %VERSION% --file-version %VERSION% --original-filename wslc-compose.exe
if errorlevel 1 (
    echo resource generation failed.
    exit /b 1
)

go build -trimpath -ldflags "-s -w -X main.version=%VERSION%" -o build\wslc-compose.exe .
if errorlevel 1 (
    echo build failed.
    exit /b 1
)

.\build\wslc-compose.exe --version
endlocal
