// Package tracker reads from the facility maintenance tracker's API: what
// products, items and supplies there are, how many, and where. The API is
// read-only and authorised with a token an admin makes on the tracker's
// API page.
package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrNotConfigured means there's no tracker address or token.
var ErrNotConfigured = errors.New("the maintenance tracker isn't set up; an admin can add it in Settings")

// ErrNotFound is the tracker saying it has no such thing.
var ErrNotFound = errors.New("not found in the maintenance tracker")

// cacheFor is how long answers are reused, so a page that shows many
// needs asks the tracker once.
const cacheFor = time.Minute

type Client struct {
	BaseURL string // e.g. "https://maintenance.example.org"
	Token   string
	HTTP    *http.Client

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	body []byte
	at   time.Time
}

func New(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		Token:   strings.TrimSpace(token),
		HTTP:    &http.Client{Timeout: 8 * time.Second},
		cache:   map[string]cached{},
	}
}

func (c *Client) Configured() bool { return c != nil && c.BaseURL != "" && c.Token != "" }

// PageURL links to a page in the tracker itself, e.g. PageURL("products", 3).
func (c *Client) PageURL(kind string, id int64) string {
	if c == nil || c.BaseURL == "" {
		return ""
	}
	return c.BaseURL + "/" + kind + "/" + strconv.FormatInt(id, 10)
}

// Forget drops cached answers, e.g. after something was changed there.
func (c *Client) Forget() {
	c.mu.Lock()
	c.cache = map[string]cached{}
	c.mu.Unlock()
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	if !c.Configured() {
		return ErrNotConfigured
	}
	u := c.BaseURL + "/api/v1" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	c.mu.Lock()
	hit, ok := c.cache[u]
	c.mu.Unlock()
	if ok && time.Since(hit.at) < cacheFor {
		return json.Unmarshal(hit.body, out)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't reach the maintenance tracker: %w", err)
	}
	defer resp.Body.Close()
	var body json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("the maintenance tracker sent something unexpected (HTTP %d)", resp.StatusCode)
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode == http.StatusUnauthorized:
		return errors.New("the maintenance tracker turned down the API token; it may have been revoked")
	case resp.StatusCode != http.StatusOK:
		var e struct {
			Error string `json:"error"`
		}
		json.Unmarshal(body, &e)
		return fmt.Errorf("the maintenance tracker said: %s (HTTP %d)", e.Error, resp.StatusCode)
	}
	c.mu.Lock()
	c.cache[u] = cached{body: body, at: time.Now()}
	c.mu.Unlock()
	return json.Unmarshal(body, out)
}

// JSON shapes, as the tracker's API sends them ---------------------------------

type PlaceRef struct {
	ID   int64  `json:"id"`
	Path string `json:"path"`
}

type Product struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Category   string `json:"category"`
	Counted    bool   `json:"counted"`
	Notes      string `json:"notes"`
	Total      int    `json:"total"`
	ItemCount  int    `json:"item_count"`
	PlaceCount int    `json:"place_count"`
	Items      []Item `json:"items,omitempty"`
}

type Unit struct {
	Number int    `json:"number"`
	ID     string `json:"id"`
	Note   string `json:"note,omitempty"`
}

type Item struct {
	ID        int64    `json:"id"`
	ProductID int64    `json:"product_id"`
	Name      string   `json:"name"`
	Category  string   `json:"category"`
	Counted   bool     `json:"counted"`
	Portable  bool     `json:"portable"`
	Quantity  int      `json:"quantity"`
	Place     PlaceRef `json:"place"`
	Units     []Unit   `json:"units,omitempty"`
}

type Supply struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	Unit      string   `json:"unit"`
	Place     PlaceRef `json:"place"`
	Reusable  bool     `json:"reusable"`
	OnHand    int      `json:"on_hand"`
	InUse     int      `json:"in_use"`
	Cleaning  int      `json:"cleaning"`
	Total     int      `json:"total"`
	ReorderAt int      `json:"reorder_at"`
	Stock     string   `json:"stock"` // "ok", "low" or "out"
	Requested *struct {
		At   string `json:"at"`
		Note string `json:"note"`
	} `json:"requested"`
}

// Calls ----------------------------------------------------------------------------

func (c *Client) Products(ctx context.Context, query string) ([]Product, error) {
	var out struct {
		Products []Product `json:"products"`
	}
	q := url.Values{}
	if query != "" {
		q.Set("q", query)
	}
	err := c.get(ctx, "/products", q, &out)
	return out.Products, err
}

// Product is a product with every item of it and where each is.
func (c *Client) Product(ctx context.Context, id int64) (*Product, error) {
	var out Product
	if err := c.get(ctx, "/products/"+strconv.FormatInt(id, 10), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Item is an item with its numbered units, unless it's only counted.
func (c *Client) Item(ctx context.Context, id int64) (*Item, error) {
	var out Item
	if err := c.get(ctx, "/items/"+strconv.FormatInt(id, 10), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Supplies(ctx context.Context, query string) ([]Supply, error) {
	var out struct {
		Supplies []Supply `json:"supplies"`
	}
	q := url.Values{}
	if query != "" {
		q.Set("q", query)
	}
	err := c.get(ctx, "/supplies", q, &out)
	return out.Supplies, err
}

// Check sees that the address and token work. It asks for a place that
// can't exist: with a good token the tracker says "not found", with a bad
// one it says so first.
func (c *Client) Check(ctx context.Context) error {
	var out struct{}
	if err := c.get(ctx, "/places/0", nil, &out); !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}
