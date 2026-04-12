# tapesim-tcmu

TCMU-based SSC tape drive emulator for Go.

`tapesim-tcmu` implements a `tcmu.SCSICmdHandler` that emulates a sequential-access (tape) SCSI device. It handles all 13 SSC/SPC commands issued by the [uiscsi-tape](https://github.com/uiscsi/uiscsi-tape) driver, backed by a [tapesim](https://github.com/uiscsi/tapesim) in-memory tape state machine.

## Supported Commands

| Command | Opcode | Description |
|---------|--------|-------------|
| TEST UNIT READY | 0x00 | Unit attention on first access |
| REWIND | 0x01 | Return to beginning of media |
| READ BLOCK LIMITS | 0x05 | Report min/max block sizes |
| READ(6) | 0x08 | Fixed or variable block read |
| WRITE(6) | 0x0A | Fixed or variable block write |
| WRITE FILEMARKS(6) | 0x10 | Write filemark separators |
| SPACE(6) | 0x11 | Position by blocks or filemarks |
| INQUIRY | 0x12 | Device identification (type 0x01, sequential-access) |
| MODE SELECT(6) | 0x15 | Set block size and compression |
| MODE SENSE(6) | 0x1A | Report block descriptor and compression page |
| READ POSITION | 0x34 | Report current tape position |
| REPORT DENSITY SUPPORT | 0x44 | Report supported density codes |

## Usage

```go
import (
    "github.com/uiscsi/tapesim"
    tcmutarget "github.com/uiscsi/tapesim-tcmu"
)

// Create tape media (1 GB capacity)
media := tapesim.NewMedia(1 << 30)

// Create handler with functional options
handler := tcmutarget.NewTapeHandler(media,
    tcmutarget.WithVendor("UISCSI", "Virtual Tape", "0001"),
)

// Or get a DevReadyFunc for use with go-tcmu OpenTCMUDevice
devReady := tcmutarget.NewTapeDevReady(media)
```

## Requirements

- Go 1.25+
- [go-tcmu](https://github.com/uiscsi/go-tcmu) — TCMU userspace handler framework
- [tapesim](https://github.com/uiscsi/tapesim) — tape state machine

## Testing

All tests use `tcmu.NewTestSCSICmd` — no kernel modules required:

```sh
go test -race ./...
```

## License

This project is licensed under the GNU General Public License v3.0 — see [LICENSE](LICENSE) for details.
