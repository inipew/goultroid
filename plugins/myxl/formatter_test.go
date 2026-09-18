package myxl

import (
	"strings"
	"testing"
)

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		bytes float64
		want  string
	}{
		{500, "500 B"},
		{1024, "1.00 KB"},
		{1048576, "1.00 MB"},
		{1073741824, "1.00 GB"},
		{5368709120, "5.00 GB"},
		{1099511627776, "1.00 TB"},
	}

	for _, tc := range tests {
		got := FormatBytes(tc.bytes)
		if got != tc.want {
			t.Errorf("FormatBytes(%v) = %q, want %q", tc.bytes, got, tc.want)
		}
	}
}

func TestRenderProgressBar(t *testing.T) {
	bar, pct := RenderProgressBar(50, 100, 10)
	if !strings.Contains(bar, "█") {
		t.Errorf("expected bar to contain filled block, got %s", bar)
	}
	if pct != "50.0%" {
		t.Errorf("expected 50.0%%, got %s", pct)
	}

	// 0%
	bar0, pct0 := RenderProgressBar(0, 100, 10)
	if !strings.Contains(bar0, "░") {
		t.Errorf("expected empty bar, got %s", bar0)
	}
	if pct0 != "0.0%" {
		t.Errorf("expected 0.0%%, got %s", pct0)
	}

	// 100%
	bar100, pct100 := RenderProgressBar(100, 100, 10)
	if pct100 != "100.0%" {
		t.Errorf("expected 100.0%%, got %s", pct100)
	}
	if strings.Contains(bar100, "░") {
		t.Errorf("expected no empty blocks at 100%%, got %s", bar100)
	}
}

func TestMaskMSISDN(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"6281912345678", "6281****5678"},
		{"081912345678", "0819****5678"},
		{"1234", "1234"},
	}
	for _, tc := range tests {
		got := MaskMSISDN(tc.input)
		if got != tc.want {
			t.Errorf("MaskMSISDN(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestFormatQuotaResponse(t *testing.T) {
	acc := &Account{
		MSISDN: "6281900001234",
		Alias:  "Main",
	}
	bal := &BalanceData{
		Remaining: 50000,
		ExpiredAt: 1735689600,
	}
	quota := &QuotaDetailsData{
		Quotas: []QuotaInfo{
			{
				Name:      "Xtra Combo Flex",
				ExpiredAt: 1735689600,
				Benefits: []BenefitInfo{
					{
						Name:      "Kuota Utama",
						DataType:  "DATA",
						Remaining: 10737418240, // 10 GB
						Total:     21474836480, // 20 GB
					},
				},
			},
		},
	}

	// Unmasked (Private Chat)
	resUnmasked := FormatQuotaResponse(acc, bal, quota, false)
	if !strings.Contains(resUnmasked, "6281900001234") {
		t.Error("expected unmasked output to contain full MSISDN")
	}
	if !strings.Contains(resUnmasked, "Rp 50.000") {
		t.Error("expected output to contain formatted rupiah Rp 50.000")
	}
	if !strings.Contains(resUnmasked, "Xtra Combo Flex") {
		t.Error("expected output to contain package name")
	}
	if !strings.Contains(resUnmasked, "10.00 GB / 20.00 GB") {
		t.Error("expected output to contain 10.00 GB / 20.00 GB")
	}

	// Masked (Group Chat)
	resMasked := FormatQuotaResponse(acc, bal, quota, true)
	if strings.Contains(resMasked, "6281900001234") {
		t.Error("expected masked output to NOT contain full MSISDN")
	}
	if !strings.Contains(resMasked, "6281****1234") {
		t.Error("expected masked output to contain 6281****1234")
	}
}

func TestFormatFamilyAndDetails(t *testing.T) {
	fam := &PackageListResponse{
		PackageFamily: PackageFamily{Name: "Xtra Combo", PackageFamilyCode: "FAM-99"},
		PackageVariants: []PackageVariant{
			{
				Name:               "Flex S",
				PackageVariantCode: "VAR-1",
				PackageOptions: []PackageOption{
					{Name: "Flex S 10GB", PackageOptionCode: "OPT-10", Price: 35000},
				},
			},
		},
	}
	outFam := FormatFamilyPackages(fam)
	if !strings.Contains(outFam, "Xtra Combo") || !strings.Contains(outFam, "Rp 35.000") || !strings.Contains(outFam, "OPT-10") {
		t.Fatalf("unexpected family format output: %s", outFam)
	}

	detail := &PackageDetailsData{
		PackageFamily: PackageFamily{Name: "Xtra Combo"},
		PackageOption: &PackageOption{Name: "Flex S 10GB", PackageOptionCode: "OPT-10", Price: 35000},
	}
	outDetail := FormatPackageDetails(detail)
	if !strings.Contains(outDetail, "Flex S 10GB") || !strings.Contains(outDetail, "Rp 35.000") {
		t.Fatalf("unexpected detail format output: %s", outDetail)
	}
}

func TestFormatSavedAndPurchase(t *testing.T) {
	pkgs := []*SavedPackage{
		{MSISDN: "6281900001234", OptionCode: "OPT-10", Name: "Flex S 10GB", Price: 35000, FamilyCode: "FAM-99"},
	}
	outSaved := FormatSavedPackages(pkgs)
	if !strings.Contains(outSaved, "Flex S 10GB") || !strings.Contains(outSaved, "Rp 35.000") {
		t.Fatalf("unexpected saved format output: %s", outSaved)
	}

	res := &SettlementResult{
		IsSuccess:       true,
		Status:          "SUCCESS",
		TransactionCode: "TRX-777",
		Deeplink:        "https://pay.ovo.id/777",
		QRCode:          "000201...",
	}
	outPurchase := FormatPurchaseResult(res, "Flex S 10GB", 35000, "OVO")
	if !strings.Contains(outPurchase, "TRX-777") || !strings.Contains(outPurchase, "https://pay.ovo.id/777") || !strings.Contains(outPurchase, "000201...") {
		t.Fatalf("unexpected purchase result output: %s", outPurchase)
	}
}
