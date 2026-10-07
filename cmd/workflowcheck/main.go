package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"time"

	"example.com/solo-0021-metrology-assurance-suite/internal/bootstrap"
)

type apiResponse struct {
	Status int
	Body   map[string]any
	Raw    string
}

type harness struct {
	baseURL string
	client  *http.Client
	server  *httptest.Server
}

func main() {
	workflow := flag.String("workflow", "", "workflow id to verify")
	flag.Parse()

	checks := map[string]func() error{
		"register-device":           checkRegisterDevice,
		"open-verification-case":    checkOpenVerificationCase,
		"record-measurement":        checkRecordMeasurement,
		"reopen-verification-case":  checkReopenVerificationCase,
		"seal-verification":         checkSealVerification,
		"record-intermediate-check": checkRecordIntermediateCheck,
		"measurement-check-gate":    checkMeasurementCheckGate,
		"measurement-check-pinning": checkMeasurementCheckPinning,
	}
	run, ok := checks[*workflow]
	if !ok {
		fmt.Fprintf(flag.CommandLine.Output(), "unknown workflow %q\n", *workflow)
		flag.Usage()
		os.Exit(2)
	}
	if err := run(); err != nil {
		fmt.Printf("workflow %s: failed: %v\n", *workflow, err)
		os.Exit(1)
	}
	fmt.Printf("workflow %s: ok\n", *workflow)
}

func checkRegisterDevice() error {
	h := newHarness()
	defer h.close()

	response, err := h.request(http.MethodPost, "/devices", map[string]any{
		"asset_tag": " met-1001 ",
		"model":     "Bench Meter",
		"room":      "Room A",
		"steward":   "Lin",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return err
	}
	id, err := nestedString(response.Body, "device", "id")
	if err != nil {
		return err
	}
	tag, err := nestedString(response.Body, "device", "asset_tag")
	if err != nil {
		return err
	}
	if tag != "MET-1001" {
		return fmt.Errorf("asset tag was not normalized: %q", tag)
	}

	read, err := h.request(http.MethodGet, "/devices/"+id, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}
	readID, err := nestedString(read.Body, "device", "id")
	if err != nil {
		return err
	}
	if readID != id {
		return fmt.Errorf("read returned device %q, expected %q", readID, id)
	}

	duplicate, err := h.request(http.MethodPost, "/devices", map[string]any{
		"asset_tag": "MET-1001",
		"model":     "Other Meter",
		"room":      "Room B",
		"steward":   "Mira",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(duplicate, http.StatusConflict); err != nil {
		return err
	}
	return expectErrorCode(duplicate, "duplicate_asset_tag")
}

func checkOpenVerificationCase() error {
	h := newHarness()
	defer h.close()

	deviceID, err := h.createDevice("MET-1101")
	if err != nil {
		return err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return err
	}
	state, err := nestedString(opened.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "awaiting_measurement" {
		return fmt.Errorf("opened case state = %q", state)
	}

	read, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}

	second, err := h.request(http.MethodPost, "/cases", map[string]any{
		"device_id": deviceID,
		"nominal":   25.0,
		"tolerance": 0.5,
		"steward":   "Qiao",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	return expectErrorCode(second, "active_case_exists")
}

func checkRecordMeasurement() error {
	h := newHarness()
	defer h.close()

	deviceID, err := h.createDevice("MET-1201")
	if err != nil {
		return err
	}
	if err := h.recordPassingCheck(deviceID); err != nil {
		return err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return err
	}
	accepted, err := h.recordMeasurement(caseID, 25.2, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(accepted, http.StatusCreated); err != nil {
		return err
	}
	state, err := nestedString(accepted.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "review_ready" {
		return fmt.Errorf("accepted measurement state = %q", state)
	}
	acceptedValue, err := nestedBool(accepted.Body, "measurements", 0, "accepted")
	if err != nil {
		return err
	}
	if !acceptedValue {
		return errors.New("in-tolerance measurement was not accepted")
	}

	second, err := h.recordMeasurement(caseID, 25.1, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(second, "state_conflict"); err != nil {
		return err
	}

	rejectDeviceID, err := h.createDevice("MET-1202")
	if err != nil {
		return err
	}
	if err := h.recordPassingCheck(rejectDeviceID); err != nil {
		return err
	}
	rejectCase, err := h.openCase(rejectDeviceID)
	if err != nil {
		return err
	}
	rejectCaseID, err := nestedString(rejectCase.Body, "case", "id")
	if err != nil {
		return err
	}
	rejected, err := h.recordMeasurement(rejectCaseID, 26.5, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(rejected, http.StatusCreated); err != nil {
		return err
	}
	rejectedState, err := nestedString(rejected.Body, "case", "state")
	if err != nil {
		return err
	}
	if rejectedState != "recheck_required" {
		return fmt.Errorf("out-of-tolerance state = %q", rejectedState)
	}
	rejectedValue, err := nestedBool(rejected.Body, "measurements", 0, "accepted")
	if err != nil {
		return err
	}
	if rejectedValue {
		return errors.New("out-of-tolerance measurement was accepted")
	}
	return nil
}

func checkReopenVerificationCase() error {
	h := newHarness()
	defer h.close()

	caseID, err := h.rejectedCase("MET-1301")
	if err != nil {
		return err
	}
	reopened, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "measurement setup was replaced",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(reopened, http.StatusOK); err != nil {
		return err
	}
	state, err := nestedString(reopened.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "awaiting_measurement" {
		return fmt.Errorf("reopened case state = %q", state)
	}
	measurements, err := nestedSlice(reopened.Body, "measurements")
	if err != nil {
		return err
	}
	if len(measurements) != 1 {
		return fmt.Errorf("reopen changed measurement history: %d entries", len(measurements))
	}

	second, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "duplicate request",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(second, "state_conflict"); err != nil {
		return err
	}

	events, err := h.request(http.MethodGet, "/cases/"+caseID+"/events", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(events, http.StatusOK); err != nil {
		return err
	}
	if !hasEvent(events.Body, "case_reopened") {
		return errors.New("reopen audit event is missing")
	}
	return nil
}

func checkSealVerification() error {
	h := newHarness()
	defer h.close()

	caseID, err := h.readyCase("MET-1401")
	if err != nil {
		return err
	}
	type outcome struct {
		response apiResponse
		err      error
	}
	results := make(chan outcome, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			response, err := h.request(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
				"reviewer": fmt.Sprintf("Reviewer-%d", index+1),
				"summary":  "accepted measurement sealed for review",
			})
			results <- outcome{response: response, err: err}
		}(index)
	}
	wait.Wait()
	close(results)

	successes := 0
	conflicts := 0
	for item := range results {
		if item.err != nil {
			return item.err
		}
		switch item.response.Status {
		case http.StatusCreated:
			successes++
		case http.StatusConflict:
			conflicts++
		default:
			return fmt.Errorf("unexpected concurrent seal status %d: %s", item.response.Status, item.response.Raw)
		}
	}
	if successes != 1 || conflicts != 1 {
		return fmt.Errorf("concurrent seal outcomes: %d success, %d conflict", successes, conflicts)
	}

	read, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}
	state, err := nestedString(read.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "sealed" {
		return fmt.Errorf("sealed case state = %q", state)
	}
	sealID, err := nestedString(read.Body, "seal", "id")
	if err != nil {
		return err
	}
	if sealID == "" {
		return errors.New("evidence seal is missing")
	}
	return nil
}

func checkRecordIntermediateCheck() error {
	h := newHarness()
	defer h.close()

	deviceID, err := h.createDevice("MET-1501")
	if err != nil {
		return err
	}

	// A validity deadline at or before the performance time is invalid and must
	// be rejected before anything lands.
	invalidWindow, err := h.request(http.MethodPost, "/devices/"+deviceID+"/checks", map[string]any{
		"result":       "passed",
		"standard":     "Reference Gauge RG-9",
		"performed_by": "Mei",
		"performed_at": time.Now().Add(-24 * time.Hour).Format(time.RFC3339),
		"valid_until":  time.Now().Add(-48 * time.Hour).Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	if err := expectStatus(invalidWindow, http.StatusBadRequest); err != nil {
		return err
	}
	if err := expectErrorCode(invalidWindow, "invalid_input"); err != nil {
		return err
	}
	history, err := h.request(http.MethodGet, "/devices/"+deviceID+"/checks", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(history, http.StatusOK); err != nil {
		return err
	}
	checks, err := nestedSlice(history.Body, "checks")
	if err != nil {
		return err
	}
	if len(checks) != 0 {
		return fmt.Errorf("rejected check left %d records on the device", len(checks))
	}
	currentID, present := history.Body["current_check_id"]
	if present && currentID != "" {
		return fmt.Errorf("rejected check was adopted on the device: %v", currentID)
	}

	// A structurally valid but already-expired check is kept on the ledger for
	// traceability yet must never become the current check.
	stale, err := h.request(http.MethodPost, "/devices/"+deviceID+"/checks", map[string]any{
		"result":       "passed",
		"standard":     "Reference Gauge RG-9",
		"performed_by": "Mei",
		"performed_at": time.Now().Add(-48 * time.Hour).Format(time.RFC3339),
		"valid_until":  time.Now().Add(-time.Hour).Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	if err := expectStatus(stale, http.StatusCreated); err != nil {
		return err
	}
	if value := stale.Body["current_check_id"]; value != nil && value != "" {
		return fmt.Errorf("already-expired check was adopted: %v", value)
	}
	if valid, _ := stale.Body["current_check_valid"].(bool); valid {
		return errors.New("already-expired check is reported valid")
	}
	staleChecks, err := nestedSlice(stale.Body, "checks")
	if err != nil {
		return err
	}
	if len(staleChecks) != 1 {
		return fmt.Errorf("expired historical check missing from ledger: %d entries", len(staleChecks))
	}
	// The rejected registration before it must have rolled back completely: the
	// first successfully committed check keeps the first allocated id.
	staleCheckID, err := nestedPathString(stale.Body, "checks", 0, "id")
	if err != nil {
		return err
	}
	if staleCheckID != "check-0001" {
		return fmt.Errorf("rejected registration was not fully rolled back, first id = %q", staleCheckID)
	}

	// A valid passing check becomes the single check in force.
	passed := time.Now().Add(-time.Hour)
	until := time.Now().Add(30 * 24 * time.Hour)
	first, err := h.request(http.MethodPost, "/devices/"+deviceID+"/checks", map[string]any{
		"result":       "passed",
		"standard":     "Reference Gauge RG-9",
		"performed_by": "Mei",
		"performed_at": passed.Format(time.RFC3339),
		"valid_until":  until.Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	if err := expectStatus(first, http.StatusCreated); err != nil {
		return err
	}
	firstCheckID, err := nestedString(first.Body, "current_check_id")
	if err != nil {
		return err
	}
	if firstCheckID == "" {
		return errors.New("passing check was not adopted as the current check")
	}
	if valid, _ := first.Body["current_check_valid"].(bool); !valid {
		return errors.New("fresh passing check is not reported valid")
	}
	firstChecks, err := nestedSlice(first.Body, "checks")
	if err != nil {
		return err
	}
	if len(firstChecks) != 2 {
		return fmt.Errorf("check history after valid pass = %d entries", len(firstChecks))
	}

	// A failed check is kept on the check ledger for traceability but must clear
	// the device's current check so it no longer has one check in force.
	failed, err := h.request(http.MethodPost, "/devices/"+deviceID+"/checks", map[string]any{
		"result":       "failed",
		"standard":     "Reference Gauge RG-9",
		"performed_by": "Mei",
		"performed_at": time.Now().Format(time.RFC3339),
		"valid_until":  time.Now().Add(7 * 24 * time.Hour).Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	if err := expectStatus(failed, http.StatusCreated); err != nil {
		return err
	}
	if value := failed.Body["current_check_id"]; value != nil && value != "" {
		return fmt.Errorf("failed check became the current check: %v", value)
	}
	if valid, _ := failed.Body["current_check_valid"].(bool); valid {
		return errors.New("device reports a valid current check after a failed check")
	}
	failedChecks, err := nestedSlice(failed.Body, "checks")
	if err != nil {
		return err
	}
	if len(failedChecks) != 3 {
		return fmt.Errorf("failed check must stay on the ledger: %d entries", len(failedChecks))
	}

	// A later passing check replaces the failed one as the single current check.
	second, err := h.request(http.MethodPost, "/devices/"+deviceID+"/checks", map[string]any{
		"result":       "passed",
		"standard":     "Reference Gauge RG-9",
		"performed_by": "Mei",
		"performed_at": time.Now().Format(time.RFC3339),
		"valid_until":  time.Now().Add(14 * 24 * time.Hour).Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusCreated); err != nil {
		return err
	}
	secondCheckID, err := nestedString(second.Body, "current_check_id")
	if err != nil {
		return err
	}
	if secondCheckID == "" || secondCheckID == firstCheckID {
		return fmt.Errorf("later pass did not replace the current check: first=%q second=%q", firstCheckID, secondCheckID)
	}
	return nil
}

func checkMeasurementCheckGate() error {
	h := newHarness()
	defer h.close()

	// A device with no check can open a case but cannot record a measurement.
	deviceID, err := h.createDevice("MET-1502")
	if err != nil {
		return err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return err
	}
	blocked, err := h.recordMeasurement(caseID, 25.1, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(blocked, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(blocked, "no_valid_check"); err != nil {
		return err
	}
	if measurements, _ := nestedSlice(blocked.Body, "measurements"); len(measurements) != 0 {
		return fmt.Errorf("blocked measurement left %d records", len(measurements))
	}

	// A failed check keeps the gate closed.
	if err := h.recordCheck(deviceID, map[string]any{
		"result":       "failed",
		"standard":     "Reference Gauge RG-9",
		"performed_by": "Mei",
		"performed_at": time.Now().Format(time.RFC3339),
		"valid_until":  time.Now().Add(7 * 24 * time.Hour).Format(time.RFC3339),
	}); err != nil {
		return err
	}
	blockedAfterFail, err := h.recordMeasurement(caseID, 25.1, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(blockedAfterFail, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(blockedAfterFail, "no_valid_check"); err != nil {
		return err
	}

	// A check that was passed but has already expired keeps the gate closed.
	if err := h.recordCheck(deviceID, map[string]any{
		"result":       "passed",
		"standard":     "Reference Gauge RG-9",
		"performed_by": "Mei",
		"performed_at": time.Now().Add(-48 * time.Hour).Format(time.RFC3339),
		"valid_until":  time.Now().Add(-time.Hour).Format(time.RFC3339),
	}); err != nil {
		return err
	}
	blockedAfterExpiry, err := h.recordMeasurement(caseID, 25.1, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(blockedAfterExpiry, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(blockedAfterExpiry, "no_valid_check"); err != nil {
		return err
	}

	// A fresh passing check opens the gate and the measurement goes through.
	if err := h.recordPassingCheck(deviceID); err != nil {
		return err
	}
	allowed, err := h.recordMeasurement(caseID, 25.1, "Rui")
	if err != nil {
		return err
	}
	return expectStatus(allowed, http.StatusCreated)
}

func checkMeasurementCheckPinning() error {
	h := newHarness()
	defer h.close()

	deviceID, err := h.createDevice("MET-1503")
	if err != nil {
		return err
	}
	if err := h.recordPassingCheck(deviceID); err != nil {
		return err
	}
	checksView, err := h.request(http.MethodGet, "/devices/"+deviceID+"/checks", nil)
	if err != nil {
		return err
	}
	firstCheckID, err := nestedString(checksView.Body, "current_check_id")
	if err != nil {
		return err
	}

	opened, err := h.openCase(deviceID)
	if err != nil {
		return err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return err
	}

	// An out-of-tolerance measurement under the first check stays pinned to it.
	rejected, err := h.recordMeasurement(caseID, 26.5, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(rejected, http.StatusCreated); err != nil {
		return err
	}
	pinnedOnRejected, err := nestedPathString(rejected.Body, "measurements", 0, "check_id")
	if err != nil {
		return err
	}
	if pinnedOnRejected != firstCheckID {
		return fmt.Errorf("rejected measurement pinned to %q, expected %q", pinnedOnRejected, firstCheckID)
	}

	// Reopen, register a replacement check, and measure again. The new
	// measurement must point at the new check while history keeps the old one.
	reopened, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "setup corrected after failed measurement",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(reopened, http.StatusOK); err != nil {
		return err
	}
	if err := h.recordPassingCheck(deviceID); err != nil {
		return err
	}
	checksView, err = h.request(http.MethodGet, "/devices/"+deviceID+"/checks", nil)
	if err != nil {
		return err
	}
	secondCheckID, err := nestedString(checksView.Body, "current_check_id")
	if err != nil {
		return err
	}
	if secondCheckID == firstCheckID {
		return errors.New("replacement check did not become current")
	}

	accepted, err := h.recordMeasurement(caseID, 25.1, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(accepted, http.StatusCreated); err != nil {
		return err
	}
	acceptedMeasurements, err := nestedSlice(accepted.Body, "measurements")
	if err != nil {
		return err
	}
	if len(acceptedMeasurements) != 2 {
		return fmt.Errorf("expected full measurement history, got %d", len(acceptedMeasurements))
	}
	firstPinned, err := nestedPathString(accepted.Body, "measurements", 0, "check_id")
	if err != nil {
		return err
	}
	secondPinned, err := nestedPathString(accepted.Body, "measurements", 1, "check_id")
	if err != nil {
		return err
	}
	if firstPinned != firstCheckID || secondPinned != secondCheckID {
		return fmt.Errorf("pinning changed: historical=%q want %q, current=%q want %q", firstPinned, firstCheckID, secondPinned, secondCheckID)
	}

	// Seal: the frozen record binds the selected accepted measurement to the
	// check it was actually performed under.
	sealed, err := h.request(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
		"reviewer": "Verifier Shen",
		"summary":  "accepted measurement sealed with its intermediate check",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(sealed, http.StatusCreated); err != nil {
		return err
	}
	sealCheckID, err := nestedString(sealed.Body, "seal", "measurement_check_id")
	if err != nil {
		return err
	}
	if sealCheckID != secondCheckID {
		return fmt.Errorf("seal froze check %q, expected %q", sealCheckID, secondCheckID)
	}

	// A later replacement check must not rewrite any pinned measurement or seal.
	if err := h.recordPassingCheck(deviceID); err != nil {
		return err
	}
	read, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}
	historicalFirst, err := nestedPathString(read.Body, "measurements", 0, "check_id")
	if err != nil {
		return err
	}
	historicalSecond, err := nestedPathString(read.Body, "measurements", 1, "check_id")
	if err != nil {
		return err
	}
	if historicalFirst != firstCheckID || historicalSecond != secondCheckID {
		return fmt.Errorf("sealed measurement history was rewritten: %q/%q", historicalFirst, historicalSecond)
	}
	sealedCheckID, err := nestedString(read.Body, "seal", "measurement_check_id")
	if err != nil {
		return err
	}
	if sealedCheckID != secondCheckID {
		return fmt.Errorf("sealed record was rewritten to %q", sealedCheckID)
	}
	return nil
}

func newHarness() *harness {
	server := httptest.NewServer(bootstrap.NewHandler())
	return &harness{
		baseURL: server.URL,
		client:  server.Client(),
		server:  server,
	}
}

func (h *harness) close() {
	h.server.Close()
}

func (h *harness) request(method, path string, body any) (apiResponse, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return apiResponse{}, err
		}
		reader = bytes.NewReader(encoded)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, h.baseURL+path, reader)
	if err != nil {
		return apiResponse{}, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := h.client.Do(request)
	if err != nil {
		return apiResponse{}, err
	}
	defer response.Body.Close()
	rawBytes, err := io.ReadAll(response.Body)
	if err != nil {
		return apiResponse{}, err
	}
	result := apiResponse{Status: response.StatusCode, Raw: string(rawBytes)}
	if len(bytes.TrimSpace(rawBytes)) > 0 {
		if err := json.Unmarshal(rawBytes, &result.Body); err != nil {
			return apiResponse{}, fmt.Errorf("decode %s %s response: %w", method, path, err)
		}
	}
	return result, nil
}

func (h *harness) createDevice(tag string) (string, error) {
	response, err := h.request(http.MethodPost, "/devices", map[string]any{
		"asset_tag": tag,
		"model":     "Bench Meter",
		"room":      "Room A",
		"steward":   "Lin",
	})
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return nestedString(response.Body, "device", "id")
}

func (h *harness) openCase(deviceID string) (apiResponse, error) {
	response, err := h.request(http.MethodPost, "/cases", map[string]any{
		"device_id": deviceID,
		"nominal":   25.0,
		"tolerance": 0.5,
		"steward":   "Qiao",
	})
	if err != nil {
		return apiResponse{}, err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return apiResponse{}, err
	}
	return response, nil
}

func (h *harness) recordMeasurement(caseID string, observed float64, technician string) (apiResponse, error) {
	return h.request(http.MethodPost, "/cases/"+caseID+"/measurements", map[string]any{
		"observed":   observed,
		"technician": technician,
	})
}

func (h *harness) recordPassingCheck(deviceID string) error {
	return h.recordCheck(deviceID, map[string]any{
		"result":       "passed",
		"standard":     "Reference Gauge RG-9",
		"performed_by": "Mei",
		"performed_at": time.Now().Add(-time.Hour).Format(time.RFC3339),
		"valid_until":  time.Now().Add(30 * 24 * time.Hour).Format(time.RFC3339),
	})
}

func (h *harness) recordCheck(deviceID string, body map[string]any) error {
	response, err := h.request(http.MethodPost, "/devices/"+deviceID+"/checks", body)
	if err != nil {
		return err
	}
	return expectStatus(response, http.StatusCreated)
}

func (h *harness) rejectedCase(tag string) (string, error) {
	deviceID, err := h.createDevice(tag)
	if err != nil {
		return "", err
	}
	if err := h.recordPassingCheck(deviceID); err != nil {
		return "", err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return "", err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return "", err
	}
	response, err := h.recordMeasurement(caseID, 26.5, "Rui")
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return caseID, nil
}

func (h *harness) readyCase(tag string) (string, error) {
	deviceID, err := h.createDevice(tag)
	if err != nil {
		return "", err
	}
	if err := h.recordPassingCheck(deviceID); err != nil {
		return "", err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return "", err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return "", err
	}
	response, err := h.recordMeasurement(caseID, 25.2, "Rui")
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return caseID, nil
}

func expectStatus(response apiResponse, expected int) error {
	if response.Status != expected {
		return fmt.Errorf("status = %d, expected %d: %s", response.Status, expected, response.Raw)
	}
	return nil
}

func expectErrorCode(response apiResponse, expected string) error {
	code, err := nestedString(response.Body, "error", "code")
	if err != nil {
		return err
	}
	if code != expected {
		return fmt.Errorf("error code = %q, expected %q", code, expected)
	}
	return nil
}

func nestedString(root map[string]any, path ...string) (string, error) {
	value, err := nestedValue(root, path...)
	if err != nil {
		return "", err
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s is not a string", strings.Join(path, "."))
	}
	return text, nil
}

func nestedPathString(root map[string]any, path ...any) (string, error) {
	value, err := nestedAny(root, path...)
	if err != nil {
		return "", err
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%v is not a string", path)
	}
	return text, nil
}

func nestedBool(root map[string]any, path ...any) (bool, error) {
	value, err := nestedAny(root, path...)
	if err != nil {
		return false, err
	}
	flag, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("%v is not a boolean", path)
	}
	return flag, nil
}

func nestedSlice(root map[string]any, key string) ([]any, error) {
	value, ok := root[key]
	if !ok {
		return nil, fmt.Errorf("missing %s", key)
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s is not an array", key)
	}
	return items, nil
}

func hasEvent(root map[string]any, kind string) bool {
	items, err := nestedSlice(root, "events")
	if err != nil {
		return false
	}
	for _, item := range items {
		event, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if event["kind"] == kind {
			return true
		}
	}
	return false
}

func nestedValue(root map[string]any, path ...string) (any, error) {
	parts := make([]any, 0, len(path))
	for _, part := range path {
		parts = append(parts, part)
	}
	return nestedAny(root, parts...)
}

func nestedAny(root map[string]any, path ...any) (any, error) {
	var current any = root
	for _, part := range path {
		switch typed := part.(type) {
		case string:
			object, ok := current.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s is not an object", typed)
			}
			value, ok := object[typed]
			if !ok {
				return nil, fmt.Errorf("missing %s", typed)
			}
			current = value
		case int:
			items, ok := current.([]any)
			if !ok {
				return nil, fmt.Errorf("index %d is not inside an array", typed)
			}
			if typed < 0 || typed >= len(items) {
				return nil, fmt.Errorf("index %d is out of range", typed)
			}
			current = items[typed]
		default:
			return nil, fmt.Errorf("unsupported path element %T", part)
		}
	}
	return current, nil
}
