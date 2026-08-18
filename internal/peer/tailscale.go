package peer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"sort"
)

const (
	defaultTailscaleExecutable = "tailscale"
	maxTailscaleStatusBytes    = 2 << 20
)

var (
	ErrTailscaleUnavailable   = errors.New("tailscale unavailable")
	ErrTailscaleStatusFailed  = errors.New("tailscale status failed")
	ErrInvalidTailscaleStatus = errors.New("invalid tailscale status")
)

type TailnetCandidate struct {
	Name      string
	Addresses []string
}

type TailscaleRunner struct {
	Executable string
	Run        func(ctx context.Context, executable string, args ...string) ([]byte, error)
}

type tailscaleStatus struct {
	Self struct {
		TailscaleIPs []string `json:"TailscaleIPs"`
	} `json:"Self"`
	Peer map[string]tailscalePeer `json:"Peer"`
}

type tailscalePeer struct {
	HostName     string   `json:"HostName"`
	DNSName      string   `json:"DNSName"`
	TailscaleIPs []string `json:"TailscaleIPs"`
	Online       bool     `json:"Online"`
	ShareeNode   bool     `json:"ShareeNode"`
}

func ParseTailscaleStatus(raw []byte) ([]TailnetCandidate, error) {
	if len(raw) > maxTailscaleStatusBytes {
		return nil, ErrInvalidTailscaleStatus
	}

	var status tailscaleStatus
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&status); err != nil {
		return nil, ErrInvalidTailscaleStatus
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, ErrInvalidTailscaleStatus
	}

	selfAddresses, err := canonicalAddresses(status.Self.TailscaleIPs)
	if err != nil {
		return nil, ErrInvalidTailscaleStatus
	}
	selfSet := make(map[string]struct{}, len(selfAddresses))
	for _, address := range selfAddresses {
		selfSet[address] = struct{}{}
	}

	candidates := make([]TailnetCandidate, 0, len(status.Peer))
	for _, peer := range status.Peer {
		if !peer.Online || peer.ShareeNode {
			continue
		}
		addresses, err := canonicalAddresses(peer.TailscaleIPs)
		if err != nil {
			return nil, ErrInvalidTailscaleStatus
		}
		if len(addresses) == 0 || sharesAddress(addresses, selfSet) {
			continue
		}
		name := peer.HostName
		if name == "" {
			name = peer.DNSName
		}
		candidates = append(candidates, TailnetCandidate{
			Name:      name,
			Addresses: addresses,
		})
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Name != candidates[j].Name {
			return candidates[i].Name < candidates[j].Name
		}
		return compareAddressLists(candidates[i].Addresses, candidates[j].Addresses) < 0
	})
	return candidates, nil
}

func (r TailscaleRunner) Status(ctx context.Context) ([]TailnetCandidate, error) {
	if ctx == nil {
		return nil, ErrTailscaleStatusFailed
	}

	executable := r.Executable
	if executable == "" {
		executable = defaultTailscaleExecutable
	}
	run := r.Run
	if run == nil {
		run = runTailscaleCommand
	}

	output, err := run(ctx, executable, "status", "--json")
	if len(output) > maxTailscaleStatusBytes {
		return nil, ErrInvalidTailscaleStatus
	}
	if err != nil {
		if isUnavailableTailscaleError(err) {
			return nil, ErrTailscaleUnavailable
		}
		if errors.Is(err, errTailscaleOutputTooLarge) {
			return nil, ErrInvalidTailscaleStatus
		}
		return nil, ErrTailscaleStatusFailed
	}
	return ParseTailscaleStatus(output)
}

func canonicalAddresses(addresses []string) ([]string, error) {
	if len(addresses) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(addresses))
	canonical := make([]string, 0, len(addresses))
	for _, raw := range addresses {
		address, err := netip.ParseAddr(raw)
		if err != nil || !address.IsValid() || address.Zone() != "" {
			return nil, ErrInvalidTailscaleStatus
		}
		value := address.String()
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		canonical = append(canonical, value)
	}
	sort.Strings(canonical)
	return canonical, nil
}

func sharesAddress(addresses []string, self map[string]struct{}) bool {
	for _, address := range addresses {
		if _, ok := self[address]; ok {
			return true
		}
	}
	return false
}

func compareAddressLists(left, right []string) int {
	limit := len(left)
	if len(right) < limit {
		limit = len(right)
	}
	for i := 0; i < limit; i++ {
		if left[i] < right[i] {
			return -1
		}
		if left[i] > right[i] {
			return 1
		}
	}
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	return 0
}

var errTailscaleOutputTooLarge = errors.New("tailscale output too large")

type limitedOutput struct {
	buffer   bytes.Buffer
	tooLarge bool
}

func (w *limitedOutput) Write(data []byte) (int, error) {
	if len(data) > maxTailscaleStatusBytes-w.buffer.Len() {
		w.tooLarge = true
		return 0, errTailscaleOutputTooLarge
	}
	return w.buffer.Write(data)
}

func runTailscaleCommand(ctx context.Context, executable string, args ...string) ([]byte, error) {
	var output limitedOutput
	command := exec.CommandContext(ctx, executable, args...)
	command.Stdout = &output
	command.Stderr = io.Discard
	err := command.Run()
	if output.tooLarge {
		return nil, errTailscaleOutputTooLarge
	}
	return output.buffer.Bytes(), err
}

func isUnavailableTailscaleError(err error) bool {
	return errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist)
}
