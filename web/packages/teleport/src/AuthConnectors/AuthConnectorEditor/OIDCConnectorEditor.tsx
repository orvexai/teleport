/**
 * Teleport
 * Copyright (C) 2026  Gravitational, Inc.
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

import { useCallback, useEffect, useState } from 'react';
import { useNavigate, useParams } from 'react-router';

import { useAsync } from 'shared/hooks/useAsync';

import cfg from 'teleport/config';
import useTeleport from 'teleport/useTeleport';

import templates from '../templates';
import { AuthConnectorEditorContent } from './AuthConnectorEditorContent';

/**
 * OIDCConnectorEditor is the edit/create page for an OIDC Auth Connector.
 */
export function OIDCConnectorEditor({ isNew = false }) {
  const { connectorName } = useParams<{ connectorName: string }>();
  const ctx = useTeleport();
  const navigate = useNavigate();

  const [content, setContent] = useState(templates['oidc']);
  const [initialContent, setInitialContent] = useState(templates['oidc']);

  const [fetchAttempt, fetchConnector] = useAsync(async () => {
    if (!isNew) {
      const res = await ctx.resourceService.fetchOidcConnector(connectorName);
      setContent(res.content);
      setInitialContent(res.content);
    }
    return;
  });

  const [saveAttempt, saveConnector] = useAsync(
    useCallback(async () => {
      if (isNew) {
        await ctx.resourceService
          .createOidcConnector(content)
          .then(() => navigate(cfg.routes.sso));
      } else {
        await ctx.resourceService
          .updateOidcConnector(connectorName, content)
          .then(() => navigate(cfg.routes.sso));
      }
    }, [connectorName, content, isNew, navigate, ctx.resourceService])
  );

  const isSaveDisabled =
    saveAttempt.status === 'processing' || content === initialContent;

  useEffect(() => {
    if (fetchAttempt.status !== 'success') {
      fetchConnector();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const title = isNew
    ? 'Creating new OIDC Auth Connector: '
    : `Editing Auth Connector: ${connectorName}`;

  return (
    <AuthConnectorEditorContent
      title={title}
      content={content}
      backButtonRoute={cfg.routes.sso}
      isSaveDisabled={isSaveDisabled}
      saveAttempt={saveAttempt}
      fetchAttempt={fetchAttempt}
      onSave={saveConnector}
      onCancel={() => navigate(cfg.routes.sso)}
      setContent={setContent}
      isGithub={false}
    />
  );
}
