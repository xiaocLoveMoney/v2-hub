package api

import (
	"context"
	"net/http"
	"net/netip"
	"time"

	"v2-hub-server/internal/service"
)

// 商店接口（docs/03 §5.12、§5.13、§5.15、§5.16），与 hub-api.yaml 的 /api/admin/traffic-packs、/api/admin/coupons、/api/admin/orders、
// /api/user/shop、/api/user/orders、/api/user/wallet 逐条对应。金额一律整数分。

const (
	queryFrom      = "from"
	queryTo        = "to"
	msgBadTimeFrom = "from 须为 RFC 3339 时间"
	msgBadTimeTo   = "to 须为 RFC 3339 时间"
)

// ShopManager 是商店的业务接口，由 service.ShopService 实现。
type ShopManager interface {
	ListTrafficPacks(ctx context.Context) ([]service.TrafficPack, error)
	CreateTrafficPack(ctx context.Context, actor *service.AdminPrincipal, in service.TrafficPackInput, ip *netip.Addr) (*service.TrafficPack, error)
	UpdateTrafficPack(ctx context.Context, actor *service.AdminPrincipal, id int64, in service.TrafficPackInput, ip *netip.Addr) (*service.TrafficPack, error)
	DeleteTrafficPack(ctx context.Context, actor *service.AdminPrincipal, id int64, ip *netip.Addr) error
	ListCoupons(ctx context.Context, pq service.PageQuery) (*service.Page[service.Coupon], error)
	CreateCoupon(ctx context.Context, actor *service.AdminPrincipal, in service.CouponInput, ip *netip.Addr) (*service.Coupon, error)
	UpdateCoupon(ctx context.Context, actor *service.AdminPrincipal, id int64, in service.CouponInput, ip *netip.Addr) (*service.Coupon, error)
	DeleteCoupon(ctx context.Context, actor *service.AdminPrincipal, id int64, ip *netip.Addr) error
	ListOrders(ctx context.Context, f service.OrderFilter, pq service.PageQuery) (*service.Page[service.Order], error)
	ListOwnOrders(ctx context.Context, p *service.UserPrincipal, pq service.PageQuery) (*service.Page[service.Order], error)
	GetShop(ctx context.Context, p *service.UserPrincipal) (*service.Shop, error)
	QuoteOrder(ctx context.Context, p *service.UserPrincipal, in service.OrderInput) (*service.Quote, error)
	CreateOrder(ctx context.Context, p *service.UserPrincipal, in service.OrderInput, ip *netip.Addr) (*service.Order, error)
	GetWallet(ctx context.Context, p *service.UserPrincipal) (*service.Wallet, error)
	SetAutoRenew(ctx context.Context, p *service.UserPrincipal, enabled bool, ip *netip.Addr) (*service.Wallet, error)
}

// ---- 流量包 ---------------------------------------------------------------------

// trafficPackBody 对应 hub-api.yaml TrafficPackInput。
type trafficPackBody struct {
	Name         string `json:"name"`
	TrafficBytes int64  `json:"trafficBytes"`
	Price        int64  `json:"price"`
	IsOnSale     bool   `json:"isOnSale"`
	SortOrder    int    `json:"sortOrder"`
}

func (b trafficPackBody) input() service.TrafficPackInput {
	return service.TrafficPackInput{Name: b.Name, TrafficBytes: b.TrafficBytes, Price: b.Price, OnSale: b.IsOnSale, SortOrder: b.SortOrder}
}

// trafficPackVO 对应 hub-api.yaml TrafficPack。
type trafficPackVO struct {
	trafficPackBody
	ID        int64     `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
}

func toTrafficPackVO(t service.TrafficPack) trafficPackVO {
	return trafficPackVO{ID: t.ID, CreatedAt: t.CreatedAt, trafficPackBody: trafficPackBody{
		Name: t.Name, TrafficBytes: t.TrafficBytes, Price: t.Price, IsOnSale: t.OnSale, SortOrder: t.SortOrder}}
}

func (h *handlers) listTrafficPacks(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	packs, err := h.shop.ListTrafficPacks(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]trafficPackVO, 0, len(packs))
	for _, t := range packs {
		out = append(out, toTrafficPackVO(t))
	}
	writeOK(w, out)
}

func (h *handlers) createTrafficPack(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req trafficPackBody
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	t, err := h.shop.CreateTrafficPack(r.Context(), p, req.input(), clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toTrafficPackVO(*t))
}

func (h *handlers) updateTrafficPack(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req trafficPackBody
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	t, err := h.shop.UpdateTrafficPack(r.Context(), p, id, req.input(), clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toTrafficPackVO(*t))
}

func (h *handlers) deleteTrafficPack(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.shop.DeleteTrafficPack(r.Context(), p, id, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

// ---- 优惠码 ---------------------------------------------------------------------

// couponBody 对应 hub-api.yaml CouponInput；maxUsesPerUser 省略时取契约默认值 1。
type couponBody struct {
	Code           string     `json:"code"`
	Kind           string     `json:"kind"`
	Value          int64      `json:"value"`
	AllPlans       bool       `json:"allPlans"`
	PlanIDs        []int64    `json:"planIds"`
	ForTrafficPack bool       `json:"forTrafficPack"`
	MinAmount      int64      `json:"minAmount"`
	MaxUses        int        `json:"maxUses"`
	MaxUsesPerUser *int       `json:"maxUsesPerUser"`
	StartsAt       *time.Time `json:"startsAt"`
	EndsAt         *time.Time `json:"endsAt"`
	IsDisabled     bool       `json:"isDisabled"`
	Remark         string     `json:"remark"`
}

func (b couponBody) input() service.CouponInput {
	perUser := defaultCouponUsesPerUser
	if b.MaxUsesPerUser != nil {
		perUser = *b.MaxUsesPerUser
	}
	return service.CouponInput{
		Code: b.Code, Kind: b.Kind, Value: b.Value, AllPlans: b.AllPlans, PlanIDs: b.PlanIDs, ForTrafficPack: b.ForTrafficPack, MinAmount: b.MinAmount,
		MaxUses: b.MaxUses, MaxUsesPerUser: perUser, StartsAt: b.StartsAt, EndsAt: b.EndsAt, Disabled: b.IsDisabled, Remark: b.Remark,
	}
}

const defaultCouponUsesPerUser = 1

// couponVO 对应 hub-api.yaml Coupon。
type couponVO struct {
	couponBody
	ID        int64     `json:"id"`
	UsedCount int       `json:"usedCount"`
	CreatedAt time.Time `json:"createdAt"`
}

func toCouponVO(c service.Coupon) couponVO {
	plans := c.PlanIDs
	if plans == nil {
		plans = []int64{}
	}
	perUser := c.MaxUsesPerUser
	return couponVO{ID: c.ID, UsedCount: c.UsedCount, CreatedAt: c.CreatedAt, couponBody: couponBody{
		Code: c.Code, Kind: c.Kind, Value: c.Value, AllPlans: c.AllPlans, PlanIDs: plans, ForTrafficPack: c.ForTrafficPack, MinAmount: c.MinAmount,
		MaxUses: c.MaxUses, MaxUsesPerUser: &perUser, StartsAt: c.StartsAt, EndsAt: c.EndsAt, IsDisabled: c.Disabled, Remark: c.Remark,
	}}
}

type couponPageVO struct {
	Items []couponVO `json:"items"`
	Total int64      `json:"total"`
}

func (h *handlers) listCoupons(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	pq, err := pageQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := h.shop.ListCoupons(r.Context(), pq)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := couponPageVO{Items: make([]couponVO, 0, len(page.Items)), Total: page.Total}
	for _, c := range page.Items {
		out.Items = append(out.Items, toCouponVO(c))
	}
	writeOK(w, out)
}

func (h *handlers) createCoupon(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	var req couponBody
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	c, err := h.shop.CreateCoupon(r.Context(), p, req.input(), clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toCouponVO(*c))
}

func (h *handlers) updateCoupon(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req couponBody
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	c, err := h.shop.UpdateCoupon(r.Context(), p, id, req.input(), clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toCouponVO(*c))
}

func (h *handlers) deleteCoupon(w http.ResponseWriter, r *http.Request, p *service.AdminPrincipal) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.shop.DeleteCoupon(r.Context(), p, id, clientIP(r, h.trustedProxies)); err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, nil)
}

// ---- 订单 ----------------------------------------------------------------------

// orderVO 对应 hub-api.yaml Order；用户侧不带 userId / username。
type orderVO struct {
	ID             int64     `json:"id"`
	OrderNo        string    `json:"orderNo"`
	Kind           string    `json:"kind"`
	ItemName       string    `json:"itemName"`
	DurationDays   int       `json:"durationDays"`
	TrafficBytes   int64     `json:"trafficBytes"`
	IsTrial        bool      `json:"isTrial"`
	AmountOriginal int64     `json:"amountOriginal"`
	AmountDiscount int64     `json:"amountDiscount"`
	AmountPaid     int64     `json:"amountPaid"`
	CouponCode     string    `json:"couponCode"`
	CreatedAt      time.Time `json:"createdAt"`
	UserID         *int64    `json:"userId,omitempty"`
	Username       *string   `json:"username,omitempty"`
}

func toOrderVO(o service.Order, isAdmin bool) orderVO {
	vo := orderVO{
		ID: o.ID, OrderNo: o.OrderNo, Kind: o.Kind, ItemName: o.ItemName, DurationDays: o.DurationDays, TrafficBytes: o.TrafficBytes, IsTrial: o.Trial,
		AmountOriginal: o.AmountOriginal, AmountDiscount: o.AmountDiscount, AmountPaid: o.AmountPaid, CouponCode: o.CouponCode, CreatedAt: o.CreatedAt,
	}
	if isAdmin {
		vo.Username = &o.Username
		if o.UserID != 0 {
			vo.UserID = &o.UserID
		}
	}
	return vo
}

type orderPageVO struct {
	Items []orderVO `json:"items"`
	Total int64     `json:"total"`
}

func toOrderPageVO(page *service.Page[service.Order], isAdmin bool) orderPageVO {
	out := orderPageVO{Items: make([]orderVO, 0, len(page.Items)), Total: page.Total}
	for _, o := range page.Items {
		out.Items = append(out.Items, toOrderVO(o, isAdmin))
	}
	return out
}

// optionalTimeQuery 读取可选的 RFC 3339 时间查询参数；省略返回 nil。
func optionalTimeQuery(r *http.Request, name, badMessage string) (*time.Time, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, badRequest(badMessage)
	}
	return &t, nil
}

func (h *handlers) listOrders(w http.ResponseWriter, r *http.Request, _ *service.AdminPrincipal) {
	pq, err := pageQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	userID, err := optionalUserID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	from, err := optionalTimeQuery(r, queryFrom, msgBadTimeFrom)
	if err != nil {
		writeError(w, r, err)
		return
	}
	to, err := optionalTimeQuery(r, queryTo, msgBadTimeTo)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := h.shop.ListOrders(r.Context(), service.OrderFilter{UserID: userID, Kind: r.URL.Query().Get(queryKind), From: from, To: to}, pq)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toOrderPageVO(page, true))
}

func (h *handlers) listOwnOrders(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	pq, err := pageQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := h.shop.ListOwnOrders(r.Context(), p, pq)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toOrderPageVO(page, false))
}

// orderBody 对应 hub-api.yaml OrderInput。
type orderBody struct {
	Kind          string `json:"kind"`
	PlanPriceID   int64  `json:"planPriceId"`
	TrafficPackID int64  `json:"trafficPackId"`
	CouponCode    string `json:"couponCode"`
}

func (b orderBody) input() service.OrderInput {
	return service.OrderInput{Kind: b.Kind, PlanPriceID: b.PlanPriceID, TrafficPackID: b.TrafficPackID, CouponCode: b.CouponCode}
}

// quoteVO 对应 hub-api.yaml Quote。
type quoteVO struct {
	AmountOriginal int64      `json:"amountOriginal"`
	AmountDiscount int64      `json:"amountDiscount"`
	AmountPayable  int64      `json:"amountPayable"`
	Balance        int64      `json:"balance"`
	Shortfall      int64      `json:"shortfall"`
	IsRenewal      bool       `json:"isRenewal"`
	ExpiresAt      *time.Time `json:"expiresAt"`
	TrafficLimit   int64      `json:"trafficLimit"`
	CouponCode     string     `json:"couponCode"`
}

func (h *handlers) quoteOrder(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	var req orderBody
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	q, err := h.shop.QuoteOrder(r.Context(), p, req.input())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, quoteVO{AmountOriginal: q.AmountOriginal, AmountDiscount: q.AmountDiscount, AmountPayable: q.AmountPayable, Balance: q.Balance,
		Shortfall: q.Shortfall, IsRenewal: q.Renewal, ExpiresAt: q.ExpiresAt, TrafficLimit: q.TrafficLimit, CouponCode: q.CouponCode})
}

func (h *handlers) createOrder(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	var req orderBody
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	o, err := h.shop.CreateOrder(r.Context(), p, req.input(), clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toOrderVO(*o, false))
}

// ---- 商店与钱包 -----------------------------------------------------------------

// shopPlanVO 对应 hub-api.yaml ShopPlan。
type shopPlanVO struct {
	ID           int64         `json:"id"`
	Name         string        `json:"name"`
	Description  string        `json:"description"`
	TrafficLimit int64         `json:"trafficLimit"`
	TrafficReset string        `json:"trafficReset"`
	LimitIP      int           `json:"limitIp"`
	NodeCount    int           `json:"nodeCount"`
	IsTrial      bool          `json:"isTrial"`
	IsCurrent    bool          `json:"isCurrent"`
	CanBuy       bool          `json:"canBuy"`
	Reason       string        `json:"reason"`
	Prices       []planPriceVO `json:"prices"`
}

// shopTrafficPackVO 对应 hub-api.yaml ShopTrafficPack。
type shopTrafficPackVO struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	TrafficBytes int64  `json:"trafficBytes"`
	Price        int64  `json:"price"`
}

// shopVO 对应 hub-api.yaml Shop。
type shopVO struct {
	Balance           int64               `json:"balance"`
	Plans             []shopPlanVO        `json:"plans"`
	TrafficPacks      []shopTrafficPackVO `json:"trafficPacks"`
	CanBuyTrafficPack bool                `json:"canBuyTrafficPack"`
	TrafficPackReason string              `json:"trafficPackReason"`
}

func (h *handlers) getShop(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	shop, err := h.shop.GetShop(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := shopVO{Balance: shop.Balance, Plans: make([]shopPlanVO, 0, len(shop.Plans)), TrafficPacks: make([]shopTrafficPackVO, 0, len(shop.TrafficPacks)),
		CanBuyTrafficPack: shop.CanBuyTrafficPack, TrafficPackReason: shop.TrafficPackReason}
	for _, sp := range shop.Plans {
		prices := make([]planPriceVO, 0, len(sp.Prices))
		for _, price := range sp.Prices {
			prices = append(prices, planPriceVO{ID: price.ID, planPriceBody: planPriceBody{DurationDays: price.DurationDays, Price: price.Price}})
		}
		out.Plans = append(out.Plans, shopPlanVO{ID: sp.ID, Name: sp.Name, Description: sp.Description, TrafficLimit: sp.TrafficLimit, TrafficReset: sp.TrafficReset,
			LimitIP: sp.LimitIP, NodeCount: sp.NodeCount, IsTrial: sp.Trial, IsCurrent: sp.Current, CanBuy: sp.CanBuy, Reason: sp.Reason, Prices: prices})
	}
	for _, t := range shop.TrafficPacks {
		out.TrafficPacks = append(out.TrafficPacks, shopTrafficPackVO{ID: t.ID, Name: t.Name, TrafficBytes: t.TrafficBytes, Price: t.Price})
	}
	writeOK(w, out)
}

type autoRenewVO struct {
	Enabled      bool   `json:"enabled"`
	Available    bool   `json:"available"`
	Reason       string `json:"reason"`
	PlanName     string `json:"planName"`
	DurationDays int    `json:"durationDays"`
	Price        *int64 `json:"price"`
}

type gatewayVO struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// walletVO 对应 hub-api.yaml Wallet。
type walletVO struct {
	Balance     int64       `json:"balance"`
	AutoRenew   autoRenewVO `json:"autoRenew"`
	Gateways    []gatewayVO `json:"gateways"`
	RebateTotal int64       `json:"rebateTotal"`
}

// toWalletVO 组装钱包总览；gateways 为空表示没有配置在线充值，只能用充值码（Ruling-28）。
func toWalletVO(wl *service.Wallet, gateways []service.GatewayInfo) walletVO {
	a := wl.AutoRenew
	out := walletVO{
		Balance: wl.Balance, RebateTotal: wl.RebateTotal, Gateways: make([]gatewayVO, 0, len(gateways)),
		AutoRenew: autoRenewVO{Enabled: a.Enabled, Available: a.Available, Reason: a.Reason, PlanName: a.PlanName, DurationDays: a.DurationDays, Price: a.Price},
	}
	for _, g := range gateways {
		out.Gateways = append(out.Gateways, gatewayVO{Code: g.Code, Name: g.Name})
	}
	return out
}

func (h *handlers) getWallet(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	wl, err := h.shop.GetWallet(r.Context(), p)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toWalletVO(wl, h.pay.Gateways()))
}

// autoRenewBody 对应 hub-api.yaml AutoRenewInput。
type autoRenewBody struct {
	Enabled bool `json:"enabled"`
}

func (h *handlers) setAutoRenew(w http.ResponseWriter, r *http.Request, p *service.UserPrincipal) {
	var req autoRenewBody
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	wl, err := h.shop.SetAutoRenew(r.Context(), p, req.Enabled, clientIP(r, h.trustedProxies))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeOK(w, toWalletVO(wl, h.pay.Gateways()))
}
