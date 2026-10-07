package assurance

import (
	"context"
	"fmt"
	"strings"
	"time"

	"example.com/solo-0021-metrology-assurance-suite/internal/contracts"
	"example.com/solo-0021-metrology-assurance-suite/internal/domain"
	"example.com/solo-0021-metrology-assurance-suite/internal/repository"
)

type Service struct {
	repository *repository.Repository
	now        func() time.Time
}

func NewService(repo *repository.Repository) *Service {
	return &Service{
		repository: repo,
		now:        time.Now,
	}
}

func (s *Service) RegisterDevice(ctx context.Context, input contracts.RegisterDeviceRequest) (domain.Device, error) {
	var created domain.Device
	tag := domain.NormalizeAssetTag(input.AssetTag)
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		if _, exists := m.DeviceByTag(tag); exists {
			return fmt.Errorf("%w: %s", domain.ErrDuplicateAssetTag, tag)
		}
		item, err := domain.NewDevice(
			m.NextID("device"),
			input.AssetTag,
			input.Model,
			input.Room,
			input.Steward,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := m.PutDevice(item); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"device",
			item.ID,
			"",
			"device_enrolled",
			item.Steward,
			"device enrolled for assurance work",
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		created = item
		return nil
	})
	return created, err
}

func (s *Service) Device(ctx context.Context, id string) (domain.Device, error) {
	var found domain.Device
	err := s.repository.Read(ctx, func(snapshot repository.Snapshot) error {
		item, exists := snapshot.Device(strings.TrimSpace(id))
		if !exists {
			return fmt.Errorf("%w: device %s", domain.ErrNotFound, id)
		}
		found = item
		return nil
	})
	return found, err
}

func (s *Service) OpenCase(ctx context.Context, input contracts.OpenCaseRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		deviceID := strings.TrimSpace(input.DeviceID)
		device, exists := m.Device(deviceID)
		if !exists {
			return fmt.Errorf("%w: device %s", domain.ErrNotFound, deviceID)
		}
		if !device.IsActive() {
			return fmt.Errorf("%w: device %s is %s", domain.ErrStateConflict, device.ID, device.Status)
		}
		if active, exists := m.ActiveCaseForDevice(device.ID); exists {
			return fmt.Errorf("%w: case %s is still %s", domain.ErrActiveCaseExists, active.ID, active.State)
		}
		item, err := domain.NewVerificationCase(
			m.NextID("case"),
			device.ID,
			input.Nominal,
			input.Tolerance,
			input.Steward,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := m.PutCase(item); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			"case_opened",
			item.Steward,
			"verification case opened",
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		result = caseResponse(m, item)
		return nil
	})
	return result, err
}

func (s *Service) Case(ctx context.Context, id string) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Read(ctx, func(snapshot repository.Snapshot) error {
		item, exists := snapshot.Case(strings.TrimSpace(id))
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		result = caseResponseFromSnapshot(snapshot, item)
		return nil
	})
	return result, err
}

func (s *Service) RecordMeasurement(ctx context.Context, caseID string, input contracts.RecordMeasurementRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		id := strings.TrimSpace(caseID)
		item, exists := m.Case(id)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		device, exists := m.Device(item.DeviceID)
		if !exists {
			return fmt.Errorf("%w: device %s", domain.ErrNotFound, item.DeviceID)
		}
		now := s.now()
		if !device.HasValidCheck(now) {
			return fmt.Errorf("%w: device %s has no passed, unexpired intermediate check", domain.ErrNoValidCheck, device.ID)
		}
		// The current check is pinned onto the measurement at record time and is
		// never rewritten by checks registered later.
		check, exists := m.Check(device.CurrentCheckID)
		if !exists || check.DeviceID != device.ID || !check.IsInForce(now) {
			return fmt.Errorf("%w: device %s current check %s is not in force", domain.ErrNoValidCheck, device.ID, device.CurrentCheckID)
		}
		measurement, err := domain.NewMeasurement(
			m.NextID("measurement"),
			item.ID,
			input.Observed,
			item.Nominal,
			item.Tolerance,
			input.Technician,
			check.ID,
			now,
		)
		if err != nil {
			return err
		}
		if err := item.RecordMeasurement(measurement.Accepted, now); err != nil {
			return err
		}
		if err := m.AppendMeasurement(measurement); err != nil {
			return err
		}
		if err := m.UpdateCase(item); err != nil {
			return err
		}
		kind := "measurement_rejected"
		detail := "measurement exceeded tolerance and requires another measurement"
		if measurement.Accepted {
			kind = "measurement_accepted"
			detail = "measurement is within tolerance and ready for review"
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			kind,
			measurement.Technician,
			detail,
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		result = caseResponse(m, item)
		return nil
	})
	return result, err
}

func (s *Service) ReopenCase(ctx context.Context, caseID string, input contracts.ReopenCaseRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		id := strings.TrimSpace(caseID)
		item, exists := m.Case(id)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		if err := item.Reopen(s.now()); err != nil {
			return err
		}
		if err := m.UpdateCase(item); err != nil {
			return err
		}
		actor := strings.TrimSpace(input.Actor)
		reason := strings.TrimSpace(input.Reason)
		if actor == "" || reason == "" {
			return fmt.Errorf("%w: actor and reason are required", domain.ErrInvalidInput)
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			"case_reopened",
			actor,
			reason,
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		result = caseResponse(m, item)
		return nil
	})
	return result, err
}

func (s *Service) RecordCheck(ctx context.Context, deviceID string, input contracts.RecordCheckRequest) (contracts.ChecksResponse, error) {
	var result contracts.ChecksResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		id := strings.TrimSpace(deviceID)
		device, exists := m.Device(id)
		if !exists {
			return fmt.Errorf("%w: device %s", domain.ErrNotFound, id)
		}
		// Validate the check before anything is written. An invalid registration
		// must fail inside this callback, so the commit rolls back and leaves no
		// check, device pointer, or event behind.
		item, err := domain.NewIntermediateCheck(
			m.NextID("check"),
			device.ID,
			domain.CheckResult(strings.TrimSpace(input.Result)),
			input.Standard,
			input.PerformedBy,
			input.PerformedAt,
			input.ValidUntil,
		)
		if err != nil {
			return err
		}
		if err := m.AppendCheck(item); err != nil {
			return err
		}
		// Only a check that is in force at registration time can be adopted as the
		// device's current check. A failed check clears the current check, so a
		// failed or expired device can never look valid; a stale, already-expired
		// pass is recorded for history but never demotes a still-valid check.
		adopted := false
		switch {
		case item.Result == domain.CheckFailed:
			device.ApplyCheck(item)
		case item.IsInForce(s.now()):
			device.ApplyCheck(item)
			adopted = true
		}
		if err := m.UpdateDevice(device); err != nil {
			return err
		}
		kind := "device_check_recorded"
		detail := "intermediate check recorded without becoming the current check"
		switch {
		case item.Result == domain.CheckFailed:
			kind = "device_check_failed"
			detail = "intermediate check failed and the device has no current check"
		case adopted:
			kind = "device_check_passed"
			detail = "intermediate check passed and adopted as the current check"
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"device",
			device.ID,
			"",
			kind,
			item.PerformedBy,
			detail,
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		result = checksResponseAt(m, device, s.now())
		return nil
	})
	if err != nil {
		return contracts.ChecksResponse{}, err
	}
	return result, nil
}

func (s *Service) Checks(ctx context.Context, deviceID string) (contracts.ChecksResponse, error) {
	var result contracts.ChecksResponse
	err := s.repository.Read(ctx, func(snapshot repository.Snapshot) error {
		id := strings.TrimSpace(deviceID)
		device, exists := snapshot.Device(id)
		if !exists {
			return fmt.Errorf("%w: device %s", domain.ErrNotFound, id)
		}
		result = checksResponseFromSnapshotAt(snapshot, device, s.now())
		return nil
	})
	return result, err
}

func (s *Service) SealCase(ctx context.Context, caseID string, input contracts.SealCaseRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		id := strings.TrimSpace(caseID)
		item, exists := m.Case(id)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		if _, exists := m.Seal(item.ID); exists {
			return fmt.Errorf("%w: case %s", domain.ErrSealAlreadyCreated, item.ID)
		}
		measurement, ok := bestAcceptedMeasurement(m.Measurements(item.ID))
		if !ok {
			return fmt.Errorf("%w: an accepted measurement is required before sealing", domain.ErrStateConflict)
		}
		pinnedCheck, exists := m.Check(measurement.CheckID)
		if !exists || pinnedCheck.DeviceID != item.DeviceID {
			return fmt.Errorf("%w: accepted measurement %s does not reference a recorded check of device %s", domain.ErrStateConflict, measurement.ID, item.DeviceID)
		}
		if err := item.Seal(s.now()); err != nil {
			return err
		}
		seal, err := domain.NewEvidenceSeal(
			m.NextID("seal"),
			item.ID,
			item.DeviceID,
			measurement.ID,
			measurement.CheckID,
			input.Reviewer,
			input.Summary,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := m.PutSeal(seal); err != nil {
			return err
		}
		if err := m.UpdateCase(item); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			"case_sealed",
			seal.Reviewer,
			seal.Summary,
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		result = caseResponse(m, item)
		return nil
	})
	return result, err
}

func bestAcceptedMeasurement(items []domain.Measurement) (domain.Measurement, bool) {
	var selected domain.Measurement
	found := false
	for _, item := range items {
		if !item.Accepted {
			continue
		}
		if !found || item.AbsoluteDeviation() < selected.AbsoluteDeviation() {
			selected = item
			found = true
		}
	}
	return selected, found
}

func caseResponse(m *repository.Mutation, item domain.VerificationCase) contracts.CaseResponse {
	result := contracts.CaseResponse{
		Case:         item,
		Measurements: m.Measurements(item.ID),
		Events:       m.Events(item.ID),
	}
	if seal, exists := m.Seal(item.ID); exists {
		result.Seal = &seal
	}
	return result
}

func caseResponseFromSnapshot(snapshot repository.Snapshot, item domain.VerificationCase) contracts.CaseResponse {
	result := contracts.CaseResponse{
		Case:         item,
		Measurements: snapshot.Measurements(item.ID),
		Events:       snapshot.Events(item.ID),
	}
	if seal, exists := snapshot.Seal(item.ID); exists {
		result.Seal = &seal
	}
	return result
}

func checksResponseAt(m *repository.Mutation, device domain.Device, now time.Time) contracts.ChecksResponse {
	return contracts.ChecksResponse{
		CurrentCheckID:    device.CurrentCheckID,
		CurrentCheckValid: device.HasValidCheck(now),
		CurrentCheckUntil: device.CurrentCheckUntil,
		Checks:            m.Checks(device.ID),
	}
}

func checksResponseFromSnapshotAt(snapshot repository.Snapshot, device domain.Device, now time.Time) contracts.ChecksResponse {
	return contracts.ChecksResponse{
		CurrentCheckID:    device.CurrentCheckID,
		CurrentCheckValid: device.HasValidCheck(now),
		CurrentCheckUntil: device.CurrentCheckUntil,
		Checks:            snapshot.Checks(device.ID),
	}
}
