package drand

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drand/drand/v2/common"
	"github.com/drand/drand/v2/common/chain"
	"github.com/drand/drand/v2/crypto"
	"github.com/drand/kyber/share"
	"github.com/drand/kyber/util/random"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	scerror "github.com/vexxvakan/vrf/sidecar/errors"
	sidecarmetrics "github.com/vexxvakan/vrf/sidecar/servers/metrics"
)

type testDrandFixture struct {
	scheme   *crypto.Scheme
	pubPoly  *share.PubPoly
	priShare *share.PriShare
	info     *chain.Info

	cfg Config

	beacons map[uint64]drandHTTPBeacon
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func newTestDrandFixture(t *testing.T) *testDrandFixture {
	t.Helper()

	scheme, err := crypto.SchemeFromName(crypto.DefaultSchemeID)
	require.NoError(t, err)

	priPoly := share.NewPriPoly(scheme.KeyGroup, 1, nil, random.New())
	pubPoly := priPoly.Commit(nil)
	priShare := priPoly.Shares(1)[0]

	genesisTime := time.Now().Add(-10 * time.Minute).Unix()
	genesisSeed := []byte("sidecar-test-seed")

	info := &chain.Info{
		PublicKey:   pubPoly.Commit(),
		ID:          "default",
		Period:      3 * time.Second,
		Scheme:      scheme.Name,
		GenesisTime: genesisTime,
		GenesisSeed: genesisSeed,
	}

	pubKeyBytes, err := info.PublicKey.MarshalBinary()
	require.NoError(t, err)

	cfg := Config{
		DrandVersionCheck: DrandVersionCheckOff,
		ChainHash:         info.Hash(),
		PublicKey:         pubKeyBytes,
		PeriodSeconds:     uint64(info.Period / time.Second),
		GenesisUnixSec:    info.GenesisTime,
	}

	sig1 := mustMakeRecoveredSig(t, scheme, pubPoly, priShare, &common.Beacon{
		Round: 1,
	})

	sig2 := mustMakeRecoveredSig(t, scheme, pubPoly, priShare, &common.Beacon{
		Round:       2,
		PreviousSig: sig1,
	})

	beacons := map[uint64]drandHTTPBeacon{
		1: {
			Round:      1,
			Signature:  hex.EncodeToString(sig1),
			Randomness: hex.EncodeToString(crypto.RandomnessFromSignature(sig1)),
		},
		2: {
			Round:             2,
			Signature:         hex.EncodeToString(sig2),
			PreviousSignature: hex.EncodeToString(sig1),
			Randomness:        hex.EncodeToString(crypto.RandomnessFromSignature(sig2)),
		},
	}

	return &testDrandFixture{
		scheme:   scheme,
		pubPoly:  pubPoly,
		priShare: priShare,
		info:     info,
		cfg:      cfg,
		beacons:  beacons,
	}
}

func mustMakeRecoveredSig(t *testing.T, scheme *crypto.Scheme, pubPoly *share.PubPoly, priShare *share.PriShare, b *common.Beacon) []byte {
	t.Helper()

	msg := scheme.DigestBeacon(b)
	partial, err := scheme.ThresholdScheme.Sign(priShare, msg)
	require.NoError(t, err)

	sig, err := scheme.ThresholdScheme.Recover(pubPoly, msg, [][]byte{partial}, 1, 1)
	require.NoError(t, err)
	return sig
}

func withHTTPRoundTripper(t *testing.T, rt http.RoundTripper) {
	t.Helper()

	old := newHTTPClient
	t.Cleanup(func() { newHTTPClient = old })

	newHTTPClient = func() *http.Client {
		return &http.Client{
			Timeout:   5 * time.Second,
			Transport: rt,
		}
	}
}

func handlerRoundTripper(h http.Handler) http.RoundTripper {
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		resp := rec.Result()
		resp.Request = req
		return resp, nil
	})
}

func newTestDrandHandler(t *testing.T, fx *testDrandFixture) (http.Handler, *atomic.Int64) {
	t.Helper()

	const latestRound uint64 = 2

	var latestCalls atomic.Int64
	chainHex := hex.EncodeToString(fx.cfg.ChainHash)

	mux := http.NewServeMux()

	mux.HandleFunc("/"+chainHex+"/info", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, fx.info.ToJSON(w, nil))
	})

	mux.HandleFunc("/"+chainHex+"/public/latest", func(w http.ResponseWriter, _ *http.Request) {
		latestCalls.Add(1)
		b, ok := fx.beacons[latestRound]
		if !ok {
			http.NotFound(w, nil)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(b))
	})

	mux.HandleFunc("/"+chainHex+"/public/", func(w http.ResponseWriter, r *http.Request) {
		roundStr := strings.TrimPrefix(r.URL.Path, "/"+chainHex+"/public/")
		parsed, err := strconv.ParseUint(roundStr, 10, 64)
		if err != nil {
			http.Error(w, "bad round", http.StatusBadRequest)
			return
		}
		b, ok := fx.beacons[parsed]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(b))
	})

	return mux, &latestCalls
}

func TestDrandService_VerifiesBeaconsAndDerivesRandomness(t *testing.T) {
	fx := newTestDrandFixture(t)
	handler, _ := newTestDrandHandler(t, fx)
	fx.cfg.DrandHTTP = "http://127.0.0.1"
	withHTTPRoundTripper(t, handlerRoundTripper(handler))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	svc, err := NewDrandService(ctx, fx.cfg, zap.NewNop(), sidecarmetrics.NewNop())
	require.NoError(t, err)

	res1, err := svc.Randomness(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, uint64(1), res1.DrandRound)
	require.Equal(t, crypto.RandomnessFromSignature(res1.Signature), res1.Randomness)

	res2, err := svc.Randomness(ctx, 2)
	require.NoError(t, err)
	require.Equal(t, uint64(2), res2.DrandRound)
	require.Equal(t, crypto.RandomnessFromSignature(res2.Signature), res2.Randomness)
	require.Equal(t, res1.Signature, res2.PreviousSignature)
}

func TestDrandService_BadSignature(t *testing.T) {
	fx := newTestDrandFixture(t)
	badSigHex := strings.Repeat("aa", 96)
	badSigBytes, err := hex.DecodeString(badSigHex)
	require.NoError(t, err)

	fx.beacons[2] = drandHTTPBeacon{
		Round:             2,
		Signature:         badSigHex,
		PreviousSignature: fx.beacons[2].PreviousSignature,
		Randomness:        hex.EncodeToString(crypto.RandomnessFromSignature(badSigBytes)),
	}

	handler, _ := newTestDrandHandler(t, fx)
	fx.cfg.DrandHTTP = "http://127.0.0.1"
	withHTTPRoundTripper(t, handlerRoundTripper(handler))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	svc, err := NewDrandService(ctx, fx.cfg, zap.NewNop(), sidecarmetrics.NewNop())
	require.NoError(t, err)

	_, err = svc.Randomness(ctx, 2)
	require.Error(t, err)
	require.True(t, errors.Is(err, scerror.ErrBadSignature))
}

func TestDrandService_HashMismatch(t *testing.T) {
	fx := newTestDrandFixture(t)
	fx.beacons[1] = drandHTTPBeacon{
		Round:      1,
		Signature:  fx.beacons[1].Signature,
		Randomness: strings.Repeat("00", 32),
	}

	handler, _ := newTestDrandHandler(t, fx)
	fx.cfg.DrandHTTP = "http://127.0.0.1"
	withHTTPRoundTripper(t, handlerRoundTripper(handler))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	svc, err := NewDrandService(ctx, fx.cfg, zap.NewNop(), sidecarmetrics.NewNop())
	require.NoError(t, err)

	_, err = svc.Randomness(ctx, 1)
	require.Error(t, err)
	require.True(t, errors.Is(err, scerror.ErrHashMismatch))
}

func TestDrandService_WrongRound(t *testing.T) {
	fx := newTestDrandFixture(t)
	fx.beacons[5] = fx.beacons[2]

	handler, _ := newTestDrandHandler(t, fx)
	fx.cfg.DrandHTTP = "http://127.0.0.1"
	withHTTPRoundTripper(t, handlerRoundTripper(handler))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	svc, err := NewDrandService(ctx, fx.cfg, zap.NewNop(), sidecarmetrics.NewNop())
	require.NoError(t, err)

	_, err = svc.Randomness(ctx, 5)
	require.Error(t, err)
	require.True(t, errors.Is(err, scerror.ErrWrongRound))
}

func TestDrandService_CachesLatest(t *testing.T) {
	fx := newTestDrandFixture(t)
	handler, latestCalls := newTestDrandHandler(t, fx)
	fx.cfg.DrandHTTP = "http://127.0.0.1"
	withHTTPRoundTripper(t, handlerRoundTripper(handler))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	svc, err := NewDrandService(ctx, fx.cfg, zap.NewNop(), sidecarmetrics.NewNop())
	require.NoError(t, err)

	_, err = svc.Randomness(ctx, 0)
	require.NoError(t, err)
	_, err = svc.Randomness(ctx, 0)
	require.NoError(t, err)

	require.Equal(t, int64(1), latestCalls.Load())
}

func TestDrandService_RandomnessRequestUsesStaticBaseAndCanonicalPath(t *testing.T) {
	fx := newTestDrandFixture(t)
	handler, _ := newTestDrandHandler(t, fx)
	fx.cfg.DrandHTTP = "http://127.0.0.1"
	baseTransport := handlerRoundTripper(handler)
	withHTTPRoundTripper(t, baseTransport)

	baseURL, err := url.Parse(fx.cfg.DrandHTTP)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	svc, err := NewDrandService(ctx, fx.cfg, zap.NewNop(), sidecarmetrics.NewNop())
	require.NoError(t, err)

	chainHex := hex.EncodeToString(fx.cfg.ChainHash)

	t.Run("latest_round_0", func(t *testing.T) {
		var captured url.URL
		haveCaptured := make(chan struct{}, 1)

		svc.httpClient.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if strings.HasSuffix(req.URL.Path, "/public/latest") {
				captured = *req.URL
				select {
				case haveCaptured <- struct{}{}:
				default:
				}
			}
			return baseTransport.RoundTrip(req)
		})

		_, err := svc.Randomness(ctx, 0)
		require.NoError(t, err)

		select {
		case <-haveCaptured:
		case <-time.After(2 * time.Second):
			t.Fatal("expected to capture drand /public/latest request URL")
		}

		require.Equal(t, baseURL.Scheme, captured.Scheme)
		require.Equal(t, baseURL.Host, captured.Host)
		require.Equal(t, "/"+chainHex+"/public/latest", captured.Path)
		require.Empty(t, captured.RawQuery)
		require.Empty(t, captured.Fragment)
	})

	t.Run("explicit_round_gt_0", func(t *testing.T) {
		const round uint64 = 2

		var captured url.URL
		haveCaptured := make(chan struct{}, 1)

		svc.httpClient.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if strings.HasSuffix(req.URL.Path, "/public/"+strconv.FormatUint(round, 10)) {
				captured = *req.URL
				select {
				case haveCaptured <- struct{}{}:
				default:
				}
			}
			return baseTransport.RoundTrip(req)
		})

		_, err := svc.Randomness(ctx, round)
		require.NoError(t, err)

		select {
		case <-haveCaptured:
		case <-time.After(2 * time.Second):
			t.Fatal("expected to capture drand /public/<round> request URL")
		}

		require.Equal(t, baseURL.Scheme, captured.Scheme)
		require.Equal(t, baseURL.Host, captured.Host)
		require.Equal(t, "/"+chainHex+"/public/"+strconv.FormatUint(round, 10), captured.Path)
		require.Empty(t, captured.RawQuery)
		require.Empty(t, captured.Fragment)
	})
}
