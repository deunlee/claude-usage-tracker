package store

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"time"

	"claudeusage/internal/claude"
	"claudeusage/internal/util"
)

type MissedReset struct {
	Field string
	Label string
	At    time.Time
}

// 프로그램이 꺼져 있는 동안 5시간·주간·모델별 주간 창이 초기화됐으면, 그래프가 다음 수집까지 옛 값에서 사선으로 내려오지 않고
// 초기화 시각에 수직으로 떨어지도록 1초 전에는 마지막 값을, 초기화 시각에는 그 창을 0%로 적은 요약을 월별 파일에 넣는다.
// 받은 응답이 아니므로 logs/와 recent.json에는 넣지 않는다.
func (a *Archive) RecordMissedResets(email string, startedAt time.Time) ([]MissedReset, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	files, err := a.filesOf(email)
	if err != nil {
		return nil, err
	}
	fetchedAt, snapshot, err := lastSnapshot(filepath.Join(files.dir, recentFileName))
	if err != nil || snapshot == nil {
		return nil, err
	}

	// 한 창만 초기화됐으면 다른 창은 마지막 값을 그대로 적어야 그 선이 0으로 떨어져 보이지 않는다.
	values := summaryValues{{name: "fiveHour"}, {name: "sevenDay"}}
	var resets []MissedReset
	for _, r := range snapshot.Readings() {
		if r.Field == nil {
			continue
		}
		values.set(*r.Field, &r.Percent)
		if r.ResetsAt == nil {
			continue
		}
		resetAt, ok := util.ParseTime(*r.ResetsAt)
		// 같은 초기화 시각이 응답마다 경계 앞뒤 1초 안쪽에서 흔들려 온다(17:59:59.999319Z, 18:00:00.072468Z).
		// 요약 시각은 초 단위라 버리면 경계보다 1초 이르게 적히므로 가장 가까운 초로 맞춘다.
		resetAt = resetAt.Round(time.Second)
		if ok && resetAt.After(fetchedAt) && !resetAt.After(startedAt) {
			resets = append(resets, MissedReset{Field: *r.Field, Label: r.Label, At: resetAt})
		}
	}
	slices.SortStableFunc(resets, func(x, y MissedReset) int { return x.At.Compare(y.At) })

	var recorded []MissedReset
	zero := 0.0
	written := fetchedAt
	for len(resets) > 0 {
		// 두 창이 같은 시각에 초기화되면 한 번에 떨어뜨린다.
		at := resets[0].At
		count := 1
		for count < len(resets) && resets[count].At.Equal(at) {
			count++
		}
		group := resets[:count]
		resets = resets[count:]

		before := slices.Clone(values)
		for _, reset := range group {
			values.set(reset.Field, &zero)
		}
		// 마지막 응답 뒤의 요약은 앞서 다시 시작했을 때 넣은 이 기록뿐이다. 수집에 성공하기 전에 또 다시 시작해도 겹쳐 넣지 않는다.
		done, err := hasSummaryAtOrAfter(filepath.Join(files.dir, monthlyFileName(at)), at)
		if err != nil {
			return recorded, err
		}
		if !done {
			// 바로 앞 기록이 1초 안쪽이면 그 기록이 이미 직전 값을 보여 준다.
			if justBefore := at.Add(-time.Second); justBefore.After(written) {
				if err := files.appendSummary(justBefore, before); err != nil {
					return recorded, err
				}
			}
			if err := files.appendSummary(at, slices.Clone(values)); err != nil {
				return recorded, err
			}
			recorded = append(recorded, group...)
		}
		written = at
	}
	return recorded, nil
}

func (f *accountFiles) appendSummary(at time.Time, values summaryValues) error {
	encoded, err := util.MarshalJSON(Summary{time: util.LocalISO(at), values: values})
	if err != nil {
		return err
	}
	return f.monthly.append(monthlyFileName(at), encoded)
}

// recent.json의 마지막 성공 응답. 파일이 없거나 성공한 응답이 없으면 snapshot이 nil이다.
func (a *Archive) LastSnapshot(email string) (time.Time, *claude.Snapshot, error) {
	dir, err := accountArchiveDir(a.root, email)
	if err != nil {
		return time.Time{}, nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return lastSnapshot(filepath.Join(dir, recentFileName))
}

// 파일이 없거나 성공한 응답이 없으면 snapshot이 nil이다.
func lastSnapshot(file string) (time.Time, *claude.Snapshot, error) {
	records, err := readJSONArray(file)
	if err != nil {
		return time.Time{}, nil, err
	}
	for _, raw := range slices.Backward(records) {
		var record struct {
			FetchedAt string          `json:"fetchedAt"`
			OK        bool            `json:"ok"`
			Data      json.RawMessage `json:"data"`
		}
		if json.Unmarshal(raw, &record) != nil || !record.OK || record.Data == nil {
			continue
		}
		fetchedAt, ok := util.ParseTime(record.FetchedAt)
		if !ok {
			continue
		}
		if snapshot, ok := claude.SnapshotOf(record.Data); ok {
			return fetchedAt, snapshot, nil
		}
	}
	return time.Time{}, nil, nil
}

func hasSummaryAtOrAfter(file string, t time.Time) (bool, error) {
	records, err := readJSONArray(file)
	if err != nil {
		return false, err
	}
	samples := parseSummaries(records)
	return len(samples) > 0 && samples[len(samples)-1].t >= t.UnixMilli(), nil
}
