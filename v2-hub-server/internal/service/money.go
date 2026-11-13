package service

// 金额一律用整数分（docs/03 §5.12，Ruling-29）：不出现浮点，单币种，币种设置只影响前端显示。
const (
	// maxMoney 是单价与单笔金额的上限（分）。
	maxMoney int64 = 99_999_999
	// maxBalance 是余额上限（分），与 end_user 的 CHECK 约束一致。
	maxBalance int64 = 9_999_999_999
	// percentBase 是百分比的分母。
	percentBase int64 = 100

	maxPlanPrices         = 6
	minPriceDays          = 1
	maxPriceDays          = 3650
	maxPlanDescriptionLen = 500

	msgBadMoney = "金额超出范围"
)
