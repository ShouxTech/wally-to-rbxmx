# wally-to-rbxmx
A tool to convert Wally packages and dependencies to Roblox Studio importable RBXMX files.

## Download
Download the latest release from the [Releases page](https://github.com/ShouxTech/wally-to-rbxmx/releases/)
Windows, macOS, and Linux prebuilt binaries are available.

## Usage:
```bash
./wally-to-rbxmx <scope/name@version> [output.rbxmx]
```

## Examples:
### Create an auto-named RBXMX file from a Wally package:
```bash
./wally-to-rbxmx shouxtech/waitfor@0.0.3
```
### Create an explicitly named RBXMX file from a Wally package:
```bash
./wally-to-rbxmx shouxtech/waitfor@0.0.3 WaitFor.rbxmx
```
