package share

import (
	"context"
	shwap_getter "github.com/celestiaorg/celestia-node/share/shwap/getter"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/p2p/net/conngater"
	"go.uber.org/fx"

	libhead "github.com/celestiaorg/go-header"
	"github.com/celestiaorg/go-header/sync"

	"github.com/celestiaorg/celestia-node/header"
	"github.com/celestiaorg/celestia-node/nodebuilder/node"
	modp2p "github.com/celestiaorg/celestia-node/nodebuilder/p2p"
	"github.com/celestiaorg/celestia-node/share"
	"github.com/celestiaorg/celestia-node/share/availability/full"
	"github.com/celestiaorg/celestia-node/share/availability/light"
	"github.com/celestiaorg/celestia-node/share/getters"
	disc "github.com/celestiaorg/celestia-node/share/p2p/discovery"
	"github.com/celestiaorg/celestia-node/share/p2p/peers"
	"github.com/celestiaorg/celestia-node/share/p2p/shrexeds"
	"github.com/celestiaorg/celestia-node/share/p2p/shrexnd"
	"github.com/celestiaorg/celestia-node/share/p2p/shrexsub"
	"github.com/celestiaorg/celestia-node/share/store"
)

func ConstructModule(tp node.Type, cfg *Config, options ...fx.Option) fx.Option {
	// sanitize config values before constructing module
	cfgErr := cfg.Validate(tp)

	baseComponents := fx.Options(
		fx.Supply(*cfg),
		fx.Error(cfgErr),
		fx.Options(options...),
		fx.Provide(newModule),
		fx.Invoke(func(disc *disc.Discovery) {}),
		fx.Provide(fx.Annotate(
			newDiscovery(cfg.Discovery),
			fx.OnStart(func(ctx context.Context, d *disc.Discovery) error {
				return d.Start(ctx)
			}),
			fx.OnStop(func(ctx context.Context, d *disc.Discovery) error {
				return d.Stop(ctx)
			}),
		)),
		fx.Provide(
			func(ctx context.Context, h host.Host, network modp2p.Network) (*shrexsub.PubSub, error) {
				return shrexsub.NewPubSub(ctx, h, network.String())
			},
		),
	)

	shrexGetterComponents := fx.Options(
		fx.Provide(func() peers.Parameters {
			return cfg.PeerManagerParams
		}),
		fx.Provide(
			func(host host.Host, network modp2p.Network) (*shrexnd.Client, error) {
				cfg.ShrExNDParams.WithNetworkID(network.String())
				return shrexnd.NewClient(cfg.ShrExNDParams, host)
			},
		),
		fx.Provide(
			func(host host.Host, network modp2p.Network) (*shrexeds.Client, error) {
				cfg.ShrExEDSParams.WithNetworkID(network.String())
				return shrexeds.NewClient(cfg.ShrExEDSParams, host)
			},
		),
		fx.Provide(fx.Annotate(
			getters.NewShrexGetter,
			fx.OnStart(func(ctx context.Context, getter *getters.ShrexGetter) error {
				return getter.Start(ctx)
			}),
			fx.OnStop(func(ctx context.Context, getter *getters.ShrexGetter) error {
				return getter.Stop(ctx)
			}),
		)),
	)

	bridgeAndFullComponents := fx.Options(
		fx.Provide(getters.NewStoreGetter),
		fx.Invoke(func(edsSrv *shrexeds.Server, ndSrc *shrexnd.Server) {}),
		fx.Provide(fx.Annotate(
			func(host host.Host, store *store.Store, network modp2p.Network) (*shrexeds.Server, error) {
				cfg.ShrExEDSParams.WithNetworkID(network.String())
				return shrexeds.NewServer(cfg.ShrExEDSParams, host, store)
			},
			fx.OnStart(func(ctx context.Context, server *shrexeds.Server) error {
				return server.Start(ctx)
			}),
			fx.OnStop(func(ctx context.Context, server *shrexeds.Server) error {
				return server.Stop(ctx)
			}),
		)),
		fx.Provide(fx.Annotate(
			func(
				host host.Host,
				store *store.Store,
				network modp2p.Network,
			) (*shrexnd.Server, error) {
				cfg.ShrExNDParams.WithNetworkID(network.String())
				return shrexnd.NewServer(cfg.ShrExNDParams, host, store)
			},
			fx.OnStart(func(ctx context.Context, server *shrexnd.Server) error {
				return server.Start(ctx)
			}),
			fx.OnStop(func(ctx context.Context, server *shrexnd.Server) error {
				return server.Stop(ctx)
			}),
		)),
		fx.Provide(fx.Annotate(
			func(path node.StorePath) (*store.Store, error) {
				return store.NewStore(cfg.EDSStoreParams, string(path))
			},
		)),
		fx.Provide(fx.Annotate(
			full.NewShareAvailability,
			fx.OnStart(func(ctx context.Context, avail *full.ShareAvailability) error {
				return avail.Start(ctx)
			}),
			fx.OnStop(func(ctx context.Context, avail *full.ShareAvailability) error {
				return avail.Stop(ctx)
			}),
		)),
		fx.Provide(func(avail *full.ShareAvailability) share.Availability {
			return avail
		}),
		fx.Provide(func(shrexSub *shrexsub.PubSub) shrexsub.BroadcastFn {
			return shrexSub.Broadcast
		}),
	)

	peerManagerWithShrexPools := fx.Options(
		fx.Provide(
			func(
				params peers.Parameters,
				host host.Host,
				connGater *conngater.BasicConnectionGater,
				shrexSub *shrexsub.PubSub,
				headerSub libhead.Subscriber[*header.ExtendedHeader],
				// we must ensure Syncer is started before PeerManager
				// so that Syncer registers header validator before PeerManager subscribes to headers
				_ *sync.Syncer[*header.ExtendedHeader],
			) (*peers.Manager, error) {
				return peers.NewManager(
					params,
					host,
					connGater,
					peers.WithShrexSubPools(shrexSub, headerSub),
				)
			},
		),
	)

	switch tp {
	case node.Bridge:
		return fx.Module(
			"share",
			baseComponents,
			fx.Provide(peers.NewManager),
			bridgeAndFullComponents,
			shrexGetterComponents,
			fx.Provide(bridgeGetter),
			fx.Invoke(func(lc fx.Lifecycle, sub *shrexsub.PubSub) error {
				lc.Append(fx.Hook{
					OnStart: sub.Start,
					OnStop:  sub.Stop,
				})
				return nil
			}),
		)
	case node.Full:
		return fx.Module(
			"share",
			peerManagerWithShrexPools,
			baseComponents,
			bridgeAndFullComponents,
			shrexGetterComponents,
			fx.Provide(shwap_getter.NewGetter),
			fx.Provide(fullGetter),
		)
	case node.Light:
		return fx.Module(
			"share",
			baseComponents,
			fx.Provide(func() []light.Option {
				return []light.Option{
					light.WithSampleAmount(cfg.LightAvailability.SampleAmount),
				}
			}),
			peerManagerWithShrexPools,
			shrexGetterComponents,
			fx.Provide(shwap_getter.NewGetter),
			fx.Provide(lightGetter),
			// shrexsub broadcaster stub for daser
			fx.Provide(func() shrexsub.BroadcastFn {
				return func(context.Context, shrexsub.Notification) error {
					return nil
				}
			}),
			fx.Provide(fx.Annotate(
				light.NewShareAvailability,
				fx.OnStop(func(ctx context.Context, la *light.ShareAvailability) error {
					return la.Close(ctx)
				}),
			)),
			fx.Provide(func(avail *light.ShareAvailability) share.Availability {
				return avail
			}),
		)
	default:
		panic("invalid node type")
	}
}
