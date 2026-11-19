package main

import (
	"time"

	"hjml/internal/engine"
)

func (a *app) downloadVersions(force bool) {
	if !a.versionLoading && len(a.versionList) == 0 {
		a.versionList, a.versionStamp = a.engine.CachedVersions()
	}
	if a.catalogContext == nil || a.versionLoading || (!force && time.Since(a.versionStamp) < engine.CatalogMaxAge) {
		return
	}
	a.versionLoading = true
	ctx := a.catalogContext
	go func() {
		versions, err := a.engine.RefreshVersions(ctx)
		if ctx.Err() != nil {
			return
		}
		a.window.Update(func() {
			a.versionLoading = false
			if err != nil {
				if len(a.versionList) == 0 || force {
					a.status = "读取 Minecraft 版本失败：" + err.Error()
				}
				return
			}
			a.versionList, a.versionStamp = versions, time.Now()
			if force {
				a.status = "Minecraft 版本列表已刷新。"
			}
		})
	}()
}

func (a *app) preloadBuilds(game string) {
	if game != "" {
		for _, loader := range loaderOptions {
			a.loadBuilds(loaderKey{game, loader.id})
		}
	}
}

func (a *app) loadBuilds(key loaderKey) {
	if a.catalogContext == nil || a.buildLoading[key] {
		return
	}
	if _, inMemory := a.loaderLists[key]; !inMemory {
		if builds, stamp, found := a.engine.CachedBuilds(key.game, key.loader); found {
			a.loaderLists[key], a.buildStamps[key] = builds, stamp
		}
	}
	if time.Since(a.buildStamps[key]) < engine.CatalogMaxAge {
		return
	}
	a.buildLoading[key] = true
	delete(a.buildErrors, key)
	ctx := a.catalogContext
	go func() {
		builds, err := a.engine.RefreshBuilds(ctx, key.game, key.loader)
		if ctx.Err() != nil {
			return
		}
		a.window.Update(func() {
			delete(a.buildLoading, key)
			if err != nil {
				a.buildErrors[key] = err.Error()
				return
			}
			a.loaderLists[key], a.buildStamps[key] = builds, time.Now()
		})
	}()
}
