import { locationUtil, UrlQueryMap, urlUtil } from '@grafana/data';
import { config, locationSearchToObject, locationService } from '@grafana/runtime';
import { sceneGraph, VizPanel } from '@grafana/scenes';
import { contextSrv } from 'app/core/core';
import { getExploreUrl } from 'app/core/utils/explore';

import { getQueryRunnerFor } from './utils';

export interface DashboardUrlOptions {
  uid?: string;
  subPath?: string;
  updateQuery?: UrlQueryMap;
  /** Set to location.search to preserve current params */
  currentQueryParams: string;
  /** * Returns solo panel route instead */
  soloRoute?: boolean;
  /** return render url */
  render?: boolean;
  /** Return an absolute URL */
  absolute?: boolean;
  // Add tz to query params
  timeZone?: string;
}

export function getDashboardUrl(options: DashboardUrlOptions) {
  let path = `/scenes/dashboard/${options.uid}${options.subPath ?? ''}`;

  if (options.soloRoute) {
    path = `/d-solo/${options.uid}${options.subPath ?? ''}`;
  }

  if (options.render) {
    path = '/render' + path;

    options.updateQuery = {
      ...options.updateQuery,
      width: 1000,
      height: 500,
      tz: options.timeZone,
    };
  }

  const params = options.currentQueryParams ? locationSearchToObject(options.currentQueryParams) : {};

  if (options.updateQuery) {
    for (const key of Object.keys(options.updateQuery)) {
      // removing params with null | undefined
      if (options.updateQuery[key] === null || options.updateQuery[key] === undefined) {
        delete params[key];
      } else {
        params[key] = options.updateQuery[key];
      }
    }
  }

  const relativeUrl = urlUtil.renderUrl(path, params);

  if (options.absolute) {
    return config.appUrl + relativeUrl.slice(1);
  }

  return relativeUrl;
}

export function getViewPanelUrl(vizPanel: VizPanel) {
  return locationUtil.getUrlForPartial(locationService.getLocation(), { viewPanel: vizPanel.state.key });
}

export function getInspectUrl(vizPanel: VizPanel) {
  return locationUtil.getUrlForPartial(locationService.getLocation(), { inspect: vizPanel.state.key });
}

export function tryGetExploreUrlForPanel(vizPanel: VizPanel): Promise<string | undefined> {
  //const dashboard = panel.getRoot();
  const panelPlugin = vizPanel.getPlugin();
  const queryRunner = getQueryRunnerFor(vizPanel);

  if (!contextSrv.hasAccessToExplore() || panelPlugin?.meta.skipDataQuery || !queryRunner) {
    return Promise.resolve(undefined);
  }

  const timeRange = sceneGraph.getTimeRange(vizPanel);

  return getExploreUrl({
    queries: queryRunner.state.queries,
    dsRef: queryRunner.state.datasource,
    timeRange: timeRange.state.value,
    scopedVars: { __sceneObject: { value: vizPanel } },
  });
}
