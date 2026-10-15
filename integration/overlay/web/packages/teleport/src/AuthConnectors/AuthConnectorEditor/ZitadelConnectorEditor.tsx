// SPDX-License-Identifier: AGPL-3.0-or-later
import { useCallback, useEffect, useState } from 'react';
import { useHistory, useParams } from 'react-router';

import { useAsync } from 'shared/hooks/useAsync';

import cfg from 'teleport/config';
import useTeleport from 'teleport/useTeleport';

import templates from '../templates';
import { AuthConnectorEditorContent } from './AuthConnectorEditorContent';

export function ZitadelConnectorEditor({ isNew = false }) {
  const { connectorName } = useParams<{ connectorName: string }>();
  const ctx = useTeleport();
  const history = useHistory();
  const [content, setContent] = useState(templates.oidc);
  const [initialContent, setInitialContent] = useState(templates.oidc);

  const [fetchAttempt, fetchConnector] = useAsync(async () => {
    if (!isNew) {
      const resource = await ctx.resourceService.fetchZitadelConnector(connectorName);
      setContent(resource.content);
      setInitialContent(resource.content);
    }
  });

  const [saveAttempt, saveConnector] = useAsync(useCallback(async () => {
    if (isNew) {
      await ctx.resourceService.createZitadelConnector(content);
    } else {
      await ctx.resourceService.updateZitadelConnector(connectorName, content);
    }
    history.push(cfg.routes.sso);
  }, [connectorName, content, isNew, history, ctx.resourceService]));

  useEffect(() => {
    fetchConnector();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <AuthConnectorEditorContent
      title={isNew ? 'Creating new Zitadel Auth Connector:' : `Editing Zitadel Auth Connector: ${connectorName}`}
      content={content}
      backButtonRoute={cfg.routes.sso}
      isSaveDisabled={saveAttempt.status === 'processing' || content === initialContent}
      saveAttempt={saveAttempt}
      fetchAttempt={fetchAttempt}
      onSave={saveConnector}
      onCancel={() => history.push(cfg.routes.sso)}
      setContent={setContent}
    />
  );
}
