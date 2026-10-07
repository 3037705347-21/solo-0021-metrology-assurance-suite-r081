package domain

import (
	"fmt"
	"strings"
	"time"
)

type DeviceStatus string

const (
	DeviceActive  DeviceStatus = "active"
	DeviceRetired DeviceStatus = "retired"
)

type Device struct {
	ID                 string       `json:"id"`
	AssetTag           string       `json:"asset_tag"`
	Model              string       `json:"model"`
	Room               string       `json:"room"`
	Steward            string       `json:"steward"`
	Status             DeviceStatus `json:"status"`
	EnrolledAt         time.Time    `json:"enrolled_at"`
	RetiredAt          *time.Time   `json:"retired_at,omitempty"`
	CurrentCheckID     string       `json:"current_check_id,omitempty"`
	CurrentCheckResult CheckResult  `json:"current_check_result,omitempty"`
	CurrentCheckUntil  time.Time    `json:"current_check_until,omitempty"`
}

type CheckResult string

const (
	CheckPassed CheckResult = "passed"
	CheckFailed CheckResult = "failed"
)

type IntermediateCheck struct {
	ID          string      `json:"id"`
	DeviceID    string      `json:"device_id"`
	Result      CheckResult `json:"result"`
	Standard    string      `json:"standard"`
	PerformedBy string      `json:"performed_by"`
	PerformedAt time.Time   `json:"performed_at"`
	ValidUntil  time.Time   `json:"valid_until"`
}

func NewIntermediateCheck(id, deviceID string, result CheckResult, standard, performedBy string, performedAt, validUntil time.Time) (IntermediateCheck, error) {
	item := IntermediateCheck{
		ID:          strings.TrimSpace(id),
		DeviceID:    strings.TrimSpace(deviceID),
		Result:      result,
		Standard:    strings.TrimSpace(standard),
		PerformedBy: strings.TrimSpace(performedBy),
		PerformedAt: performedAt.UTC(),
		ValidUntil:  validUntil.UTC(),
	}
	if err := item.Validate(); err != nil {
		return IntermediateCheck{}, err
	}
	return item, nil
}

func (c IntermediateCheck) Validate() error {
	if c.ID == "" || c.DeviceID == "" || c.Standard == "" || c.PerformedBy == "" {
		return fmt.Errorf("%w: check id, device id, standard, and performer are required", ErrInvalidInput)
	}
	switch c.Result {
	case CheckPassed, CheckFailed:
	default:
		return fmt.Errorf("%w: unsupported check result %q", ErrInvalidInput, c.Result)
	}
	if c.PerformedAt.IsZero() || c.ValidUntil.IsZero() {
		return fmt.Errorf("%w: check performance and validity times are required", ErrInvalidInput)
	}
	if !c.ValidUntil.After(c.PerformedAt) {
		return fmt.Errorf("%w: check validity deadline must be after the performance time", ErrInvalidInput)
	}
	return nil
}

// IsInForce reports whether the check passed and is inside its validity window
// at now: the check has already been performed and has not expired.
func (c IntermediateCheck) IsInForce(now time.Time) bool {
	stamp := now.UTC()
	return c.Result == CheckPassed &&
		!c.PerformedAt.After(stamp) &&
		c.ValidUntil.After(stamp)
}

func NewDevice(id, assetTag, model, room, steward string, at time.Time) (Device, error) {
	device := Device{
		ID:         strings.TrimSpace(id),
		AssetTag:   NormalizeAssetTag(assetTag),
		Model:      strings.TrimSpace(model),
		Room:       strings.TrimSpace(room),
		Steward:    strings.TrimSpace(steward),
		Status:     DeviceActive,
		EnrolledAt: at.UTC(),
	}
	if err := device.Validate(); err != nil {
		return Device{}, err
	}
	return device, nil
}

func NormalizeAssetTag(value string) string {
	return strings.ToUpper(strings.TrimSpace(value))
}

func (d Device) Validate() error {
	if d.ID == "" || d.AssetTag == "" || d.Model == "" || d.Room == "" || d.Steward == "" {
		return fmt.Errorf("%w: device id, asset tag, model, room, and steward are required", ErrInvalidInput)
	}
	if len(d.AssetTag) > 64 {
		return fmt.Errorf("%w: asset tag is too long", ErrInvalidInput)
	}
	if d.EnrolledAt.IsZero() {
		return fmt.Errorf("%w: enrollment time is required", ErrInvalidInput)
	}
	switch d.Status {
	case DeviceActive, DeviceRetired:
		return nil
	default:
		return fmt.Errorf("%w: unsupported device state %q", ErrInvalidInput, d.Status)
	}
}

func (d Device) IsActive() bool {
	return d.Status == DeviceActive
}

func (d *Device) Retire(at time.Time) error {
	if d.Status != DeviceActive {
		return fmt.Errorf("%w: only an active device can be retired", ErrStateConflict)
	}
	stamp := at.UTC()
	d.Status = DeviceRetired
	d.RetiredAt = &stamp
	return nil
}

// ApplyCheck adopts a check as the device's current check. A failed check can
// never be the check in force: it clears any previously adopted check so that
// one device has at most one check that could currently be valid.
func (d *Device) ApplyCheck(check IntermediateCheck) {
	if check.Result != CheckPassed {
		d.CurrentCheckID = ""
		d.CurrentCheckResult = ""
		d.CurrentCheckUntil = time.Time{}
		return
	}
	d.CurrentCheckID = check.ID
	d.CurrentCheckResult = CheckPassed
	d.CurrentCheckUntil = check.ValidUntil.UTC()
}

// HasValidCheck reports whether the device has a passed check whose validity
// window has not expired at now.
func (d Device) HasValidCheck(now time.Time) bool {
	return d.CurrentCheckID != "" &&
		d.CurrentCheckResult == CheckPassed &&
		!d.CurrentCheckUntil.IsZero() &&
		d.CurrentCheckUntil.After(now.UTC())
}
