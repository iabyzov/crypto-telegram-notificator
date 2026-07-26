package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"
)

type AssetInfo struct {
	Symbol      string
	Name        string
	Description string
	Platform    string
	Tags        []string
	Category    string
	URLs        map[string]string
}

type AssetInfoService struct {
	cmcAPIKey string
	cache     *redis.Client
	cacheTTL  time.Duration
}

func NewAssetInfoService(cmcAPIKey string, cache *redis.Client, cacheTTL time.Duration) *AssetInfoService {
	return &AssetInfoService{cmcAPIKey: cmcAPIKey, cache: cache, cacheTTL: cacheTTL}
}

type cmcInfo struct {
	Name        string `json:"name"`
	Symbol      string `json:"symbol"`
	Description string `json:"description"`
	Platform    *struct {
		Name         string `json:"name"`
		TokenAddress string `json:"token_address"`
	} `json:"platform"` // object or null for native coins
	Tags     []string            `json:"tags"`
	Category string              `json:"category"`
	URLs     map[string][]string `json:"urls"` // values are arrays
}
type cmcResponse struct {
	Data map[string][]cmcInfo `json:"data"` // symbol -> array (a symbol can map to multiple coins/contracts)
}

func (s *AssetInfoService) GetAssetInfo(ctx context.Context, symbol string) (*AssetInfo, error) {
	var cacheResult AssetInfo
	if s.cache.Get(ctx, fmt.Sprintf("asset:info:%s", symbol)).Scan(&cacheResult) == nil {
		return &cacheResult, nil
	}

	req, err := http.NewRequestWithContext(ctx, "GET", "https://pro-api.coinmarketcap.com/v2/cryptocurrency/info", nil)
	if err != nil {
		return nil, err
	}

	req.Header.Add("X-CMC_PRO_API_KEY", s.cmcAPIKey)

	q := req.URL.Query()
	q.Add("symbol", symbol)
	req.URL.RawQuery = q.Encode()

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}

	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to get asset info: status code %d", res.StatusCode)
	}

	var response cmcResponse

	if err := json.NewDecoder(res.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("Couldn't read from api response %v", err)
	}

	cmcInfos, exist := response.Data[symbol]
	if !exist || len(cmcInfos) == 0 {
		return nil, fmt.Errorf("Requested asset %v wasn't provided in response %v", symbol, response.Data)
	}

	assetInfo := mapToAssetInfo(cmcInfos[0])
	assetInfoBytes, err := json.Marshal(assetInfo)
	if err != nil {
		return nil, err
	}
	if err := s.cache.Set(ctx, fmt.Sprintf("asset:info:%s", symbol), assetInfoBytes, s.cacheTTL).Err(); err != nil {
		log.Printf("warn: cache set failed %v", err)
	}

	return &assetInfo, nil
}

func mapToAssetInfo(cmc cmcInfo) AssetInfo {
	res := AssetInfo{
		Symbol:      cmc.Symbol,
		Name:        cmc.Name,
		Description: cmc.Description,
		Tags:        cmc.Tags,
		Category:    cmc.Category,
		URLs:        make(map[string]string, len(cmc.URLs)),
	}

	if cmc.Platform != nil {
		res.Platform = cmc.Platform.Name
		if cmc.Platform.TokenAddress != "" {
			res.Platform = fmt.Sprintf("%s (%s)", cmc.Platform.Name, cmc.Platform.TokenAddress)
		}
	}

	for k, arr := range cmc.URLs {
		if len(arr) > 0 {
			res.URLs[k] = arr[0]
		}
	}
	return res
}
