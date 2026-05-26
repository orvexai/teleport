/**
 * Teleport
 * Copyright (C) 2023  Gravitational, Inc.
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 */

import { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate, useParams } from 'react-router';

import { Alert, Box, Flex, Indicator } from 'design';
import { H2 } from 'design/Text/Text';
import {
  InfoGuideButton,
  InfoParagraph,
  ReferenceLinks,
} from 'shared/components/SlidingSidePanel/InfoGuide';
import { useAsync } from 'shared/hooks/useAsync';

import {
  ResponsiveAddButton,
  ResponsiveFeatureHeader,
} from 'teleport/AuthConnectors/styles/AuthConnectors.styles';
import { FeatureBox, FeatureHeaderTitle } from 'teleport/components/Layout';
import { Route, Switch } from 'teleport/components/Router';
import useResources from 'teleport/components/useResources';
import cfg from 'teleport/config';
import {
  DefaultAuthConnector,
  KindAuthConnectors,
  Resource,
} from 'teleport/services/resources';
import useTeleport from 'teleport/useTeleport';

import { GitHubConnectorEditor, OIDCConnectorEditor } from './AuthConnectorEditor';
import { ConnectorList } from './ConnectorList';
import DeleteConnectorDialog from './DeleteConnectorDialog';
import EmptyList from './EmptyList';
import templates from './templates';

export const description =
  'Auth connectors allow Teleport to authenticate users via an external identity source such as Okta, Microsoft Entra ID, GitHub, etc. This authentication method is commonly known as single sign-on (SSO).';

/**
 * ConnectorEditorRouter selects the correct editor based on the connectorType route param.
 */
function ConnectorEditorRouter({ isNew = false }) {
  const { connectorType } = useParams<{ connectorType: KindAuthConnectors }>();
  if (connectorType === 'oidc') {
    return <OIDCConnectorEditor isNew={isNew} />;
  }
  // Default to GitHub editor for 'github' (and any unrecognised type).
  return <GitHubConnectorEditor isNew={isNew} />;
}

/**
 * AuthConnectorsContainer is the container for the Auth Connectors feature and handles routing to the relevant page based on the URL.
 */
export function AuthConnectorsContainer() {
  return (
    <Switch>
      <Route
        key="auth-connector-edit"
        path={cfg.routes.ssoConnector.edit}
        element={<ConnectorEditorRouter />}
      />
      <Route
        key="auth-connector-new"
        path={cfg.routes.ssoConnector.create}
        element={<ConnectorEditorRouter isNew={true} />}
      />
      <Route
        key="auth-connector-list"
        path={cfg.routes.sso}
        exact
        element={<AuthConnectors />}
      />
    </Switch>
  );
}

/**
 * AuthConnectors is the auth connectors list page.
 */
export function AuthConnectors() {
  const ctx = useTeleport();
  const [items, setItems] = useState<Resource<KindAuthConnectors>[]>([]);
  const [defaultConnector, setDefaultConnector] =
    useState<DefaultAuthConnector>();

  const [fetchAttempt, fetchConnectors] = useAsync(
    useCallback(async () => {
      const [githubRes, oidcRes] = await Promise.all([
        ctx.resourceService.fetchGithubConnectors(),
        ctx.resourceService.fetchOidcConnectors(),
      ]);
      // Merge connectors from both providers into a single list.
      const merged: Resource<KindAuthConnectors>[] = [
        ...githubRes.connectors,
        ...oidcRes.connectors,
      ];
      setItems(merged);
      // Prefer the defaultConnector from GitHub response; fall back to OIDC.
      const defaultConn =
        githubRes.defaultConnector.type !== 'local'
          ? githubRes.defaultConnector
          : oidcRes.defaultConnector;
      setDefaultConnector(defaultConn);
    }, [ctx.resourceService])
  );

  const [setDefaultAttempt, updateDefaultConnector] = useAsync(
    async (connector: DefaultAuthConnector) =>
      await ctx.resourceService.setDefaultAuthConnector(connector)
  );

  function onUpdateDefaultConnector(connector: DefaultAuthConnector) {
    const originalDefault = defaultConnector;
    setDefaultConnector(connector);
    updateDefaultConnector(connector).catch(err => {
      // Revert back to the original default if the operation failed.
      setDefaultConnector(originalDefault);
      throw err;
    });
  }

  function remove(name: string, kind: KindAuthConnectors) {
    if (kind === 'oidc') {
      return ctx.resourceService.deleteOidcConnector(name).then(fetchConnectors);
    }
    return ctx.resourceService.deleteGithubConnector(name).then(fetchConnectors);
  }

  useEffect(() => {
    if (fetchAttempt.status !== 'success') {
      fetchConnectors();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const navigate = useNavigate();
  const isEmpty = items.length === 0;
  const resources = useResources(items, templates);

  // Calculate the next default connector.
  const nextDefaultConnector = useMemo(() => {
    // If there is only one (or no) connectors, the fallback will always be "local"
    if (items.length < 2) {
      return 'Local Connector';
    }
    // If the connector being removed is last in the list, the next default will be the second last connector.
    if (items[items.length - 1].name === resources?.item?.name) {
      return items[items.length - 2].name;
    } else {
      // If the connector being removed isn't the last connector, the next default will always be the last connector.
      return items[items.length - 1].name;
    }
  }, [items, resources.item]);

  return (
    <FeatureBox>
      <ResponsiveFeatureHeader>
        <FeatureHeaderTitle>Auth Connectors</FeatureHeaderTitle>
        <InfoGuideButton config={{ guide: <InfoGuide /> }}>
          <Flex gap={2}>
            <ResponsiveAddButton
              fill="border"
              onClick={() => navigate(cfg.getCreateAuthConnectorRoute('github'))}
            >
              New GitHub Connector
            </ResponsiveAddButton>
            <ResponsiveAddButton
              fill="border"
              onClick={() => navigate(cfg.getCreateAuthConnectorRoute('oidc'))}
            >
              New OIDC Connector
            </ResponsiveAddButton>
          </Flex>
        </InfoGuideButton>
      </ResponsiveFeatureHeader>
      {fetchAttempt.status === 'error' && (
        <Alert>{fetchAttempt.statusText}</Alert>
      )}
      {fetchAttempt.status === 'processing' && (
        <Box textAlign="center" m={10}>
          <Indicator />
        </Box>
      )}
      {fetchAttempt.status === 'success' && (
        <Flex alignItems="start">
          <Flex flexDirection="column" width="100%" gap={5}>
            <Box>
              <H2 mb={4}>Your Connectors</H2>
              {setDefaultAttempt.status === 'error' && (
                <Alert>
                  Failed to set connector as default:{' '}
                  {setDefaultAttempt.statusText}
                </Alert>
              )}
              {isEmpty ? (
                <EmptyList
                  onCreate={() =>
                    navigate(cfg.getCreateAuthConnectorRoute('github'))
                  }
                  isLocalDefault={defaultConnector.type === 'local'}
                />
              ) : (
                <ConnectorList
                  items={items}
                  onDelete={resources.remove}
                  defaultConnector={defaultConnector}
                  setAsDefault={onUpdateDefaultConnector}
                />
              )}
            </Box>
          </Flex>
        </Flex>
      )}
      {resources.status === 'removing' && (
        <DeleteConnectorDialog
          name={resources.item.name}
          kind={resources.item.kind}
          onClose={resources.disregard}
          onDelete={() =>
            remove(resources.item.name, resources.item.kind as KindAuthConnectors)
          }
          isDefault={defaultConnector.name === resources.item.name}
          nextDefault={nextDefaultConnector}
        />
      )}
    </FeatureBox>
  );
}

export const InfoGuide = ({ isGitHub = false }: { isGitHub?: boolean }) => (
  <Box>
    <InfoParagraph>
      Auth connectors allow Teleport to authenticate users via an external
      identity source such as Okta, Microsoft Entra ID, GitHub, etc. This
      authentication method is commonly known as single sign-on (SSO).
    </InfoParagraph>
    <ReferenceLinks
      links={[
        {
          title: 'Configure OIDC connector',
          href: 'https://goteleport.com/docs/zero-trust-access/sso/integrate-idp/',
        },
        isGitHub
          ? {
              title: 'Configure GitHub connector',
              href: 'https://goteleport.com/docs/zero-trust-access/sso/integrate-idp/github-sso/',
            }
          : {
              title: 'Samples of different connectors',
              href: 'https://goteleport.com/docs/zero-trust-access/sso/integrate-idp/#integrating-your-provider',
            },
      ]}
    />
  </Box>
);
