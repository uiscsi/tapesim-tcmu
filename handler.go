// Package tcmutarget implements a TCMU SCSI command handler for a virtual
// SSC-3 tape drive backed by a [tapesim.Media] instance.
//
// TapeHandler implements [tcmu.SCSICmdHandler] and dispatches SCSI commands
// to the appropriate handler methods. Simple commands (TUR, INQUIRY, READ
// BLOCK LIMITS, REWIND, READ POSITION) are fully implemented here. Data
// transfer commands (READ 6, WRITE 6, WRITE FILEMARKS, SPACE) and mode page
// commands (MODE SENSE 6, MODE SELECT 6, REPORT DENSITY SUPPORT) are
// implemented in plans 02 and 03.
//
// Pre-dispatch error injection via [tapesim.Media.ConsumeInjectedError] allows
// tests to inject CHECK CONDITION responses before any command logic runs.
package tcmutarget

import (
	"encoding/binary"

	tcmu "github.com/uiscsi/go-tcmu"
	"github.com/uiscsi/go-tcmu/scsi"
	"github.com/uiscsi/tapesim"
)

// TapeHandler handles SCSI commands for a virtual SSC-3 tape drive.
// It is NOT goroutine-safe; use [NewTapeDevReady] (which wraps it in
// [tcmu.SingleThreadedDevReady]) for sequential command dispatch.
type TapeHandler struct {
	media       *tapesim.Media
	inq         tcmu.InquiryInfo
	densityCode byte
}

// HandlerOption configures a TapeHandler during construction.
type HandlerOption func(*TapeHandler)

// WithVendor overrides the INQUIRY vendor ID, product ID, and product revision
// strings. Strings are padded or truncated to the standard INQUIRY field widths.
func WithVendor(vendor, product, revision string) HandlerOption {
	return func(h *TapeHandler) {
		h.inq.VendorID = vendor
		h.inq.ProductID = product
		h.inq.ProductRev = revision
	}
}

// WithDensityCode sets the SSC-3 density code reported in MODE SENSE responses.
func WithDensityCode(code byte) HandlerOption {
	return func(h *TapeHandler) {
		h.densityCode = code
	}
}

// NewTapeHandler creates a TapeHandler backed by the given media. Default
// INQUIRY strings identify the device as "UISCSI / Virtual Tape / 0001" with
// peripheral device type 0x01 (sequential access / tape).
func NewTapeHandler(media *tapesim.Media, opts ...HandlerOption) *TapeHandler {
	h := &TapeHandler{
		media: media,
		inq: tcmu.InquiryInfo{
			VendorID:   "UISCSI",
			ProductID:  "Virtual Tape",
			ProductRev: "0001",
			DeviceType: scsi.DeviceTypeTape, // 0x01
		},
		densityCode: 0x00,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// NewTapeDevReady creates a [tcmu.DevReadyFunc] that processes SCSI commands
// sequentially through a TapeHandler. Tape drives are inherently sequential
// devices, so [tcmu.SingleThreadedDevReady] is the correct dispatch model.
func NewTapeDevReady(media *tapesim.Media, opts ...HandlerOption) tcmu.DevReadyFunc {
	h := NewTapeHandler(media, opts...)
	return tcmu.SingleThreadedDevReady(h)
}

// HandleCommand dispatches the incoming SCSI command to the appropriate handler.
// Pre-dispatch error injection is checked first: if an error is queued for the
// command's opcode, it is consumed and returned as CHECK CONDITION before any
// command logic runs.
func (h *TapeHandler) HandleCommand(cmd *tcmu.SCSICmd) (tcmu.SCSIResponse, error) {
	if s, ok := h.media.ConsumeInjectedError(cmd.GetCDB(0)); ok && s != nil {
		return cmd.RespondSenseData(0x02, tapesim.EncodeFixedSense(s)), nil
	}
	switch cmd.Command() {
	case scsi.TestUnitReady:
		return h.handleTUR(cmd)
	case scsi.Inquiry:
		return h.handleInquiry(cmd)
	case scsi.ReadBlockLimits:
		return h.handleReadBlockLimits(cmd)
	case scsi.Write6:
		return h.handleWrite(cmd)
	case scsi.Read6:
		return h.handleRead(cmd)
	case scsi.WriteFilemarks:
		return h.handleWriteFilemarks(cmd)
	case scsi.Rewind:
		return h.handleRewind(cmd)
	case scsi.ReadPosition:
		return h.handleReadPosition(cmd)
	case scsi.ModeSense:
		return h.handleModeSense6(cmd)
	case scsi.ModeSelect:
		return h.handleModeSelect6(cmd)
	case scsi.Space:
		return h.handleSpace(cmd)
	case scsi.ReportDensitySupport:
		return h.handleReportDensitySupport(cmd)
	default:
		return cmd.NotHandled(), nil
	}
}

// handleTUR implements TEST UNIT READY (opcode 0x00). It consumes any pending
// UNIT ATTENTION sense before returning GOOD. The first call after construction
// with [tapesim.WithUnitAttention] returns CHECK CONDITION (key 0x06, ASC 0x28).
func (h *TapeHandler) handleTUR(cmd *tcmu.SCSICmd) (tcmu.SCSIResponse, error) {
	if s := h.media.CheckUnitAttention(); s != nil {
		return cmd.RespondSenseData(0x02, tapesim.EncodeFixedSense(s)), nil
	}
	return cmd.Ok(), nil
}

// handleInquiry implements INQUIRY (opcode 0x12).
//
// Standard INQUIRY (EVPD=0, page code=0) builds the 36-byte response manually
// to set the Removable Medium bit (byte 1 bit 7, RMB) required for tape devices
// per SPC-4 -- [tcmu.EmulateStdInquiry] does not set this bit.
//
// EVPD INQUIRY for page 0x00 returns the supported VPD page list (0x00 only).
// Page 0x83 is not supported because it calls cmd.Device() which is nil in test
// mode and would panic.
func (h *TapeHandler) handleInquiry(cmd *tcmu.SCSICmd) (tcmu.SCSIResponse, error) {
	evpd := cmd.GetCDB(1) & 0x01
	if evpd != 0 {
		pageCode := cmd.GetCDB(2)
		if pageCode == 0x00 {
			// Supported VPD pages: 0x00 only (skip 0x83 which needs Device).
			data := make([]byte, 5)
			data[0] = scsi.DeviceTypeTape
			data[3] = 1 // page list length
			data[4] = 0x00
			if _, err := cmd.Write(data); err != nil {
				return cmd.MediumError(), nil
			}
			return cmd.Ok(), nil
		}
		return cmd.IllegalRequest(), nil
	}
	// Standard INQUIRY (EVPD=0, page code=0).
	buf := make([]byte, 36)
	buf[0] = scsi.DeviceTypeTape // byte 0: peripheral device type = 0x01
	buf[1] = 0x80                // byte 1: RMB (Removable) bit set for tape (SPC-4 6.4.2)
	buf[2] = 0x05                // byte 2: SPC-3 version
	buf[3] = 0x02                // byte 3: response data format = 2
	buf[4] = 31                  // byte 4: additional length (36 - 5 = 31)
	buf[7] = 0x02                // byte 7: CmdQue
	copy(buf[8:16], tcmu.FixedString(h.inq.VendorID, 8))
	copy(buf[16:32], tcmu.FixedString(h.inq.ProductID, 16))
	copy(buf[32:36], tcmu.FixedString(h.inq.ProductRev, 4))
	if _, err := cmd.Write(buf); err != nil {
		return cmd.MediumError(), nil
	}
	return cmd.Ok(), nil
}

// handleReadBlockLimits implements READ BLOCK LIMITS (opcode 0x05).
// Returns a 6-byte response with minimum and maximum block sizes per SSC-3.
func (h *TapeHandler) handleReadBlockLimits(cmd *tcmu.SCSICmd) (tcmu.SCSIResponse, error) {
	minBS, maxBS := h.media.BlockLimits()
	resp := make([]byte, 6)
	// byte 0: granularity = 0
	resp[1] = byte(maxBS >> 16)
	resp[2] = byte(maxBS >> 8)
	resp[3] = byte(maxBS)
	resp[4] = byte(minBS >> 8)
	resp[5] = byte(minBS)
	if _, err := cmd.Write(resp); err != nil {
		return cmd.MediumError(), nil
	}
	return cmd.Ok(), nil
}

// handleRewind implements REWIND (opcode 0x01 / RezeroUnit alias per SSC-3).
func (h *TapeHandler) handleRewind(cmd *tcmu.SCSICmd) (tcmu.SCSIResponse, error) {
	if s := h.media.Rewind(); s != nil {
		return cmd.RespondSenseData(0x02, tapesim.EncodeFixedSense(s)), nil
	}
	return cmd.Ok(), nil
}

// handleReadPosition implements READ POSITION (opcode 0x34).
// Returns a 20-byte short-form response per SSC-3 section 6.21.
// Byte 0 has BOP flag (bit 7) set when at beginning of tape.
// Bytes 4-7 carry the current logical position.
func (h *TapeHandler) handleReadPosition(cmd *tcmu.SCSICmd) (tcmu.SCSIResponse, error) {
	pi := h.media.ReadPosition()
	resp := make([]byte, 20)
	if pi.BOP {
		resp[0] = 0x80
	}
	binary.BigEndian.PutUint32(resp[4:8], uint32(pi.Position))
	if _, err := cmd.Write(resp); err != nil {
		return cmd.MediumError(), nil
	}
	return cmd.Ok(), nil
}

// handleWrite implements WRITE(6) (opcode 0x0A) per SSC-3.
//
// CDB layout: [0x0A, flags, count[2], count[3], count[4], control]
//   - flags bit 0: Fixed=1 (fixed-block mode)
//   - count: 3-byte big-endian; in fixed mode = block count, in variable mode = byte count
//
// In fixed mode the transfer length is blockCount * blockSize bytes. The data
// is read from the initiator via cmd.Read (Data-Out direction per Pitfall 5).
//
// EOM early-warning (Key=0x00, EOM) and VOLUME OVERFLOW (Key=0x0D) both
// return CHECK CONDITION per SSC-3 and Pitfall 1.
func (h *TapeHandler) handleWrite(cmd *tcmu.SCSICmd) (tcmu.SCSIResponse, error) {
	xferLen := uint32(cmd.GetCDB(2))<<16 | uint32(cmd.GetCDB(3))<<8 | uint32(cmd.GetCDB(4))
	fixed := cmd.GetCDB(1)&0x01 != 0

	var byteCount int
	if fixed {
		byteCount = int(xferLen) * h.media.BlockSize()
	} else {
		byteCount = int(xferLen)
	}
	if byteCount == 0 {
		return cmd.Ok(), nil
	}

	buf := make([]byte, byteCount)
	if _, err := cmd.Read(buf); err != nil {
		return cmd.MediumError(), nil
	}

	_, sense := h.media.Write(buf, fixed)
	if sense != nil {
		// Both EOM early-warning (Key=0x00) and VOLUME OVERFLOW (Key=0x0D)
		// return CHECK CONDITION per SSC-3 and Pitfall 1.
		return cmd.RespondSenseData(0x02, tapesim.EncodeFixedSense(sense)), nil
	}
	return cmd.Ok(), nil
}

// handleRead implements READ(6) (opcode 0x08) per SSC-3.
//
// CDB layout: [0x08, flags, count[2], count[3], count[4], control]
//   - flags bit 0: Fixed=1 (fixed-block mode)
//   - flags bit 1: SILI=1 (Suppress Incorrect Length Indicator)
//   - count: same as WRITE(6)
//
// In variable-block mode, [tapesim.Media.Read] returns exactly one record per
// call with ILI sense when the buffer size does not match the record size.
// This handler forwards that sense directly without generating additional ILI.
//
// Data-before-sense: when a short read returns partial data (n>0) alongside
// ILI or FM sense, the partial data is written via cmd.Write BEFORE returning
// CHECK CONDITION (Pitfall 2).
//
// SILI suppression: if SILI is set and the sense is ILI-only (Key=0x00,
// ILI=true, no FM), the sense is suppressed and GOOD is returned.
func (h *TapeHandler) handleRead(cmd *tcmu.SCSICmd) (tcmu.SCSIResponse, error) {
	xferLen := uint32(cmd.GetCDB(2))<<16 | uint32(cmd.GetCDB(3))<<8 | uint32(cmd.GetCDB(4))
	fixed := cmd.GetCDB(1)&0x01 != 0
	sili := cmd.GetCDB(1)&0x02 != 0

	var byteCount int
	if fixed {
		byteCount = int(xferLen) * h.media.BlockSize()
	} else {
		byteCount = int(xferLen)
	}
	if byteCount == 0 {
		return cmd.Ok(), nil
	}

	buf := make([]byte, byteCount)
	n, sense := h.media.Read(buf, fixed)
	if sense != nil {
		// SILI suppression: if SILI is set and sense is ILI-only (no FM, no error key),
		// suppress the sense and return the data as GOOD.
		if sili && sense.ILI && sense.Key == 0x00 && !sense.FM {
			if n > 0 {
				if _, err := cmd.Write(buf[:n]); err != nil {
					return cmd.MediumError(), nil
				}
			}
			return cmd.Ok(), nil
		}
		// Data-before-sense: write partial data FIRST per Pitfall 2.
		if n > 0 {
			cmd.Write(buf[:n]) //nolint:errcheck
		}
		return cmd.RespondSenseData(0x02, tapesim.EncodeFixedSense(sense)), nil
	}
	if _, err := cmd.Write(buf[:n]); err != nil {
		return cmd.MediumError(), nil
	}
	return cmd.Ok(), nil
}

// handleWriteFilemarks implements WRITE FILEMARKS(6) (opcode 0x10) per SSC-3.
//
// CDB layout: [0x10, 0, count[2], count[3], count[4], control]
//   - count: 3-byte big-endian filemark count
func (h *TapeHandler) handleWriteFilemarks(cmd *tcmu.SCSICmd) (tcmu.SCSIResponse, error) {
	count := int(uint32(cmd.GetCDB(2))<<16 | uint32(cmd.GetCDB(3))<<8 | uint32(cmd.GetCDB(4)))
	if sense := h.media.WriteFilemarks(count); sense != nil {
		return cmd.RespondSenseData(0x02, tapesim.EncodeFixedSense(sense)), nil
	}
	return cmd.Ok(), nil
}

// handleSpace implements SPACE(6) (opcode 0x11) per SSC-3.
//
// CDB layout: [0x11, code, count[2], count[3], count[4], control]
//   - code bits 0-2: space code (0=blocks, 1=filemarks, 2=sequential filemarks, 3=EOD)
//   - count: 24-bit signed (two's complement) via [tapesim.DecodeSPACECount]
func (h *TapeHandler) handleSpace(cmd *tcmu.SCSICmd) (tcmu.SCSIResponse, error) {
	code := cmd.GetCDB(1) & 0x07
	count := tapesim.DecodeSPACECount(cmd.GetCDB(2), cmd.GetCDB(3), cmd.GetCDB(4))
	if sense := h.media.Space(code, count); sense != nil {
		return cmd.RespondSenseData(0x02, tapesim.EncodeFixedSense(sense)), nil
	}
	return cmd.Ok(), nil
}

// handleModeSense6 implements MODE SENSE(6) (opcode 0x1A) per SSC-3.
//
// Returns 4-byte header + 8-byte block descriptor, plus the compression page
// (0x0F, 16 bytes) when pageCode is 0x0F or 0x3F (all pages).
//
// Header byte 0 (mode data length) = total response length minus 1,
// per the MODE SENSE pitfall documented in SSC-10.
func (h *TapeHandler) handleModeSense6(cmd *tcmu.SCSICmd) (tcmu.SCSIResponse, error) {
	pageCode := cmd.GetCDB(2) & 0x3F
	allocLen := int(cmd.GetCDB(4))

	// Build response: 4-byte header + 8-byte block descriptor.
	resp := make([]byte, 12)
	resp[3] = 8 // block descriptor length

	// Block descriptor.
	density := h.media.DensityCode()
	resp[4] = density
	bs := h.media.BlockSize()
	resp[9] = byte(bs >> 16)
	resp[10] = byte(bs >> 8)
	resp[11] = byte(bs)

	// Append compression page (0x0F) if requested.
	if pageCode == 0x0F || pageCode == 0x3F {
		page := make([]byte, 16)
		page[0] = 0x0F // page code
		page[1] = 0x0E // page length (14 data bytes)
		dce, dde := h.media.Compression()
		if dce {
			page[2] |= 0x80 // DCE bit 7
		}
		if dde {
			page[2] |= 0x40 // DDE bit 6 (same byte as DCE per SSC-3)
		}
		resp = append(resp, page...)
	}

	// Mode data length = total response size minus byte 0 itself.
	resp[0] = byte(len(resp) - 1)

	// Truncate to allocation length.
	if allocLen < len(resp) {
		resp = resp[:allocLen]
	}

	if _, err := cmd.Write(resp); err != nil {
		return cmd.MediumError(), nil
	}
	return cmd.Ok(), nil
}

// handleModeSelect6 implements MODE SELECT(6) (opcode 0x15) per SSC-3.
//
// Parses the 4-byte header for block descriptor length, then the 8-byte
// block descriptor (if present) to update media block size, then any mode
// pages (compression page 0x0F) to update DCE/DDE flags.
//
// Bounds checking guards against malformed parameter lists per T-12-08.
func (h *TapeHandler) handleModeSelect6(cmd *tcmu.SCSICmd) (tcmu.SCSIResponse, error) {
	paramLen := int(cmd.GetCDB(4))
	if paramLen == 0 {
		return cmd.Ok(), nil
	}
	// A valid MODE SELECT(6) parameter list requires at least a 4-byte header.
	if paramLen < 4 {
		return cmd.CheckCondition(scsi.SenseIllegalRequest, scsi.AscParameterListLengthError), nil
	}

	buf := make([]byte, paramLen)
	n, err := cmd.Read(buf)
	if err != nil || n < paramLen {
		return cmd.CheckCondition(scsi.SenseIllegalRequest, scsi.AscParameterListLengthError), nil
	}

	// n < 4 check removed — paramLen >= 4 is guaranteed above.
	bdLen := int(buf[3]) // block descriptor length

	// Parse block descriptor if present (8 bytes).
	offset := 4
	if bdLen >= 8 && offset+8 <= n {
		// Block length from bytes 5-7 of the block descriptor.
		bs := int(buf[offset+5])<<16 | int(buf[offset+6])<<8 | int(buf[offset+7])
		h.media.SetBlockSize(bs)
		offset += bdLen
	} else if bdLen > 0 {
		offset += bdLen
	}

	// Parse mode pages.
	for offset+2 <= n {
		pc := buf[offset] & 0x3F
		pageLen := int(buf[offset+1])
		if offset+2+pageLen > n {
			break
		}
		if pc == 0x0F && pageLen >= 2 {
			// Compression page: DCE=byte2 bit 7, DDE=byte2 bit 6 (SSC-3).
			dce := buf[offset+2]&0x80 != 0
			dde := buf[offset+2]&0x40 != 0
			h.media.SetCompression(dce, dde)
		}
		offset += 2 + pageLen
	}

	return cmd.Ok(), nil
}

// handleReportDensitySupport implements REPORT DENSITY SUPPORT (opcode 0x44)
// per SSC-3.
//
// Returns a 4-byte header followed by one 52-byte density descriptor. The
// density code is taken from h.densityCode if non-zero, otherwise from
// h.media.DensityCode(). Response is truncated to the CDB allocation length
// to prevent out-of-bounds writes per T-12-10.
func (h *TapeHandler) handleReportDensitySupport(cmd *tcmu.SCSICmd) (tcmu.SCSIResponse, error) {
	// Build one 52-byte density descriptor.
	desc := make([]byte, 52)
	code := h.densityCode
	if code == 0 {
		code = h.media.DensityCode()
	}
	desc[0] = code        // primary density code
	desc[1] = 0x00        // secondary density code
	desc[2] = 0x80 | 0x20 // WRTok=1, DefLT=1
	// bytes 5-7: bits per mm = 0 (virtual)
	// bytes 8-9: media width = 0 (virtual)
	// bytes 10-11: tracks = 0 (virtual)
	binary.BigEndian.PutUint32(desc[12:16], 1024) // capacity in MB (arbitrary)
	copy(desc[16:24], padRight("UISCSI", 8))
	copy(desc[24:44], padRight("Virtual Tape", 20))
	copy(desc[44:52], padRight("VTape", 8))

	// Header: 4 bytes.
	listLen := len(desc) // 52
	resp := make([]byte, 4+listLen)
	binary.BigEndian.PutUint16(resp[0:2], uint16(listLen))
	copy(resp[4:], desc)

	// Truncate to allocation length from CDB bytes 7-8 (10-byte CDB).
	allocLen := int(cmd.GetCDB(7))<<8 | int(cmd.GetCDB(8))
	if allocLen < len(resp) {
		resp = resp[:allocLen]
	}

	if _, err := cmd.Write(resp); err != nil {
		return cmd.MediumError(), nil
	}
	return cmd.Ok(), nil
}

// padRight pads s with spaces to exactly length n, or truncates to n.
func padRight(s string, n int) []byte {
	b := []byte(s)
	if len(b) >= n {
		return b[:n]
	}
	pad := make([]byte, n)
	copy(pad, b)
	for i := len(b); i < n; i++ {
		pad[i] = ' '
	}
	return pad
}
