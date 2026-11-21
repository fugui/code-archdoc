import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { BrowserRouter, Link, Navigate, Route, Routes, useLocation } from 'react-router-dom';
import {
  Cpu,
  FileText,
  FolderGit2,
  LayoutDashboard,
  ListChecks,
  ListTree,
  LogOut,
  Moon,
  Radar,
  Sun,
} from 'lucide-react';
import {
  AUTH_TOKEN_KEY,
  ConfirmProvider,
  ErrorBoundary,
  ToastProvider,
  UnifiedLogin,
  setupFetchInterceptor,
  useTheme,
} from '@code/common';
import './index.css';
import { apiRequest } from './api/client';
import {
  MODULE_BASE_PATH,
  MODULE_DESC,
  MODULE_KEY,
  MODULE_SUBTITLE,
  MODULE_TITLE,
} from './constants';
import { ModuleUserProvider, useModuleUser } from './context/ModuleUserContext';
import AdminDocTypesPage from './pages/AdminDocTypesPage';
import AdminLLMConfigPage from './pages/AdminLLMConfigPage';
import ArchTreeCoveragePage from './pages/ArchTreeCoveragePage';
import CanvasStudioPage from './pages/CanvasStudioPage';
import DriftRadarPage from './pages/DriftRadarPage';
import QualityMetricsPage from './pages/QualityMetricsPage';
import RepoCoveragePage from './pages/RepoCoveragePage';
import TaskCenterPage from './pages/TaskCenterPage';
import type { CurrentUserProfile } from './types';

// 在微前端嵌入模式下自动将 /api/* 重写为 /archdoc/api/* 并注入 Bearer Token
setupFetchInterceptor({ appPrefix: MODULE_BASE_PATH });

export interface AppProps {
  /** CodeBench 宿主动态挂载时会自动注入 isEmbedded={true} */
  isEmbedded?: boolean;
}

/**
 * 嵌入模式路由视图：无独立侧边栏与顶栏，同时兼容相对子路径与 /archdoc/* 完整前缀
 */
const EmbeddedRoutes: React.FC = () => {
  const defaultTarget = `${MODULE_BASE_PATH}/overview`;
  return (
    <Routes>
      <Route path="/" element={<Navigate to={defaultTarget} replace />} />
      <Route path={MODULE_BASE_PATH} element={<Navigate to={defaultTarget} replace />} />
      <Route path={`${MODULE_BASE_PATH}/`} element={<Navigate to={defaultTarget} replace />} />

      <Route path="/overview" element={<QualityMetricsPage />} />
      <Route path={`${MODULE_BASE_PATH}/overview`} element={<QualityMetricsPage />} />

      <Route path="/repos" element={<RepoCoveragePage />} />
      <Route path={`${MODULE_BASE_PATH}/repos`} element={<RepoCoveragePage />} />

      <Route path="/arch-tree" element={<ArchTreeCoveragePage />} />
      <Route path={`${MODULE_BASE_PATH}/arch-tree`} element={<ArchTreeCoveragePage />} />

      <Route path="/tasks" element={<TaskCenterPage />} />
      <Route path={`${MODULE_BASE_PATH}/tasks`} element={<TaskCenterPage />} />

      <Route path="/drift" element={<DriftRadarPage />} />
      <Route path={`${MODULE_BASE_PATH}/drift`} element={<DriftRadarPage />} />

      <Route path="/metrics" element={<QualityMetricsPage />} />
      <Route path={`${MODULE_BASE_PATH}/metrics`} element={<QualityMetricsPage />} />

      <Route path="/studio/:repoId" element={<CanvasStudioPage />} />
      <Route path={`${MODULE_BASE_PATH}/studio/:repoId`} element={<CanvasStudioPage />} />
      <Route path="/studio/:repoId/docs/:docId" element={<CanvasStudioPage />} />
      <Route
        path={`${MODULE_BASE_PATH}/studio/:repoId/docs/:docId`}
        element={<CanvasStudioPage />}
      />

      {/* 门户深链直达入口：承接 code-bench D-03 源码证据跳转 */}
      <Route path="/docs/:docId/evidence" element={<CanvasStudioPage evidenceDeepLink />} />
      <Route
        path={`${MODULE_BASE_PATH}/docs/:docId/evidence`}
        element={<CanvasStudioPage evidenceDeepLink />}
      />

      <Route path="/admin/llm" element={<AdminLLMConfigPage />} />
      <Route path={`${MODULE_BASE_PATH}/admin/llm`} element={<AdminLLMConfigPage />} />

      <Route path="/admin/doc-types" element={<AdminDocTypesPage />} />
      <Route path={`${MODULE_BASE_PATH}/admin/doc-types`} element={<AdminDocTypesPage />} />

      <Route path="*" element={<Navigate to={defaultTarget} replace />} />
    </Routes>
  );
};

/**
 * 独立调试模式布局外壳：自带导航、双模亮/暗主题切换器与本地登录守卫
 */
const StandaloneShell: React.FC<{ onLogout: () => void }> = ({ onLogout }) => {
  const location = useLocation();
  const { theme, toggleTheme } = useTheme('light');
  const { user, loadingUser, reloadUser } = useModuleUser();

  if (loadingUser) {
    return <div className="code-archdoc-loading">正在校验登录会话状态...</div>;
  }

  if (!user) {
    return (
      <UnifiedLogin
        systemName={`${MODULE_TITLE} (${MODULE_KEY})`}
        systemSubtitle={MODULE_SUBTITLE}
        systemDesc={MODULE_DESC}
        onLoginSuccess={() => {
          void reloadUser();
        }}
      />
    );
  }

  const pathname = location.pathname;
  const isOverview =
    pathname === '/' || pathname.startsWith('/overview') || pathname.startsWith('/metrics');
  const isRepos = pathname.startsWith('/repos');
  const isArchTree = pathname.startsWith('/arch-tree');
  const isTasks = pathname.startsWith('/tasks');
  const isStudio = pathname.startsWith('/studio') || pathname.startsWith('/docs/');
  const isDrift = pathname.startsWith('/drift');
  const isAdminLLM = pathname.startsWith('/admin/llm');
  const isAdminDocTypes = pathname.startsWith('/admin/doc-types');

  const topbarTitle = isAdminDocTypes
    ? '文档类型管理 (DocType 注册表)'
    : isAdminLLM
      ? 'LLM 模型配置与探活'
      : isTasks
        ? '反推任务中心'
        : isArchTree
          ? '架构文档全景'
          : isDrift
            ? '章节级双维漂移雷达'
            : isOverview
              ? '全局概览 · 文档治理与质量度量大盘'
              : isStudio
                ? 'Canvas Studio 协同审校工作台'
                : '仓库文档覆盖';

  return (
    <div className="code-archdoc-shell">
      <aside className="code-archdoc-sidebar">
        <div className="code-archdoc-sidebar-brand">
          <div className="code-archdoc-brand-logo">A</div>
          <div className="code-archdoc-brand-meta">
            <span className="code-archdoc-brand-title">{MODULE_TITLE}</span>
            <span className="code-archdoc-brand-badge">独立调试模式</span>
          </div>
        </div>

        <nav className="code-archdoc-nav">
          <div className="code-archdoc-nav-group">
            <div className="code-archdoc-nav-group-title">文档全景</div>
            <Link
              to="/overview"
              className={`code-archdoc-nav-link ${
                isOverview ? 'code-archdoc-nav-link--active' : ''
              }`}
            >
              <LayoutDashboard size={17} />
              <span>全局概览</span>
            </Link>
            <Link
              to="/repos"
              className={`code-archdoc-nav-link ${
                isRepos || isStudio ? 'code-archdoc-nav-link--active' : ''
              }`}
            >
              <FolderGit2 size={17} />
              <span>仓库文档覆盖</span>
            </Link>
            <Link
              to="/arch-tree"
              className={`code-archdoc-nav-link ${
                isArchTree ? 'code-archdoc-nav-link--active' : ''
              }`}
            >
              <ListTree size={17} />
              <span>架构文档全景</span>
            </Link>
          </div>

          <div className="code-archdoc-nav-group">
            <div className="code-archdoc-nav-group-title">生产与保鲜</div>
            <Link
              to="/tasks"
              className={`code-archdoc-nav-link ${
                isTasks ? 'code-archdoc-nav-link--active' : ''
              }`}
            >
              <ListChecks size={17} />
              <span>反推任务中心</span>
            </Link>
            <Link
              to="/drift"
              className={`code-archdoc-nav-link ${
                isDrift ? 'code-archdoc-nav-link--active' : ''
              }`}
            >
              <Radar size={17} />
              <span>双维漂移雷达</span>
            </Link>
          </div>

          {user.is_module_admin && (
            <div className="code-archdoc-nav-group">
              <div className="code-archdoc-nav-group-title">模块治理</div>
              <Link
                to="/admin/llm"
                className={`code-archdoc-nav-link ${
                  isAdminLLM ? 'code-archdoc-nav-link--active' : ''
                }`}
              >
                <Cpu size={17} />
                <span>LLM 模型配置</span>
              </Link>
              <Link
                to="/admin/doc-types"
                className={`code-archdoc-nav-link ${
                  isAdminDocTypes ? 'code-archdoc-nav-link--active' : ''
                }`}
              >
                <FileText size={17} />
                <span>文档类型管理</span>
              </Link>
            </div>
          )}
        </nav>
      </aside>

      <div className="code-archdoc-main">
        <header className="code-archdoc-topbar">
          <h2 className="code-archdoc-topbar-title">{topbarTitle}</h2>
          <div className="code-archdoc-topbar-actions">
            <button
              type="button"
              className="btn btn-secondary btn-small"
              onClick={toggleTheme}
              title="切换亮色 / 暗黑主题以验证双模视觉兼容性"
            >
              {theme === 'dark' ? <Sun size={14} /> : <Moon size={14} />}
              <span>{theme === 'dark' ? '切换亮色' : '切换暗黑'}</span>
            </button>

            <span className="code-badge code-badge--primary">
              {user.name || user.username}
            </span>

            <button type="button" className="btn btn-outline btn-small" onClick={onLogout}>
              <LogOut size={14} />
              <span>退出</span>
            </button>
          </div>
        </header>

        <div className="flex-1">
          <Routes>
            <Route path="/" element={<Navigate to="/overview" replace />} />
            <Route path="/overview" element={<QualityMetricsPage />} />
            <Route path="/repos" element={<RepoCoveragePage />} />
            <Route path="/arch-tree" element={<ArchTreeCoveragePage />} />
            <Route path="/tasks" element={<TaskCenterPage />} />
            <Route path="/drift" element={<DriftRadarPage />} />
            <Route path="/metrics" element={<QualityMetricsPage />} />
            <Route path="/studio/:repoId" element={<CanvasStudioPage />} />
            <Route path="/studio/:repoId/docs/:docId" element={<CanvasStudioPage />} />
            <Route path="/docs/:docId/evidence" element={<CanvasStudioPage evidenceDeepLink />} />
            <Route path="/admin/llm" element={<AdminLLMConfigPage />} />
            <Route path="/admin/doc-types" element={<AdminDocTypesPage />} />
            <Route path="*" element={<Navigate to="/overview" replace />} />
          </Routes>
        </div>
      </div>
    </div>
  );
};

export const App: React.FC<AppProps> = ({ isEmbedded = false }) => {
  const effectiveEmbedded = isEmbedded || Boolean(window.__POWERED_BY_PORTAL__);
  const [user, setUser] = useState<CurrentUserProfile | null>(null);
  const [loadingUser, setLoadingUser] = useState<boolean>(true);

  const loadUser = useCallback(async () => {
    const token = localStorage.getItem(AUTH_TOKEN_KEY);
    if (!token) {
      setUser(null);
      setLoadingUser(false);
      return;
    }
    try {
      const profile = await apiRequest<CurrentUserProfile>('/me');
      setUser(profile);
    } catch {
      setUser(null);
    } finally {
      setLoadingUser(false);
    }
  }, []);

  useEffect(() => {
    void loadUser();
    const handleAuthChange = () => {
      void loadUser();
    };
    window.addEventListener('auth-change', handleAuthChange);
    return () => window.removeEventListener('auth-change', handleAuthChange);
  }, [loadUser]);

  const handleStandaloneLogout = useCallback(() => {
    localStorage.removeItem(AUTH_TOKEN_KEY);
    setUser(null);
    window.location.href = window.location.pathname.startsWith(MODULE_BASE_PATH)
      ? `${MODULE_BASE_PATH}/`
      : '/';
  }, []);

  const contextValue = useMemo(
    () => ({
      user,
      loadingUser,
      isEmbedded: effectiveEmbedded,
      reloadUser: loadUser,
    }),
    [user, loadingUser, effectiveEmbedded, loadUser],
  );

  const standaloneBasename = useMemo(() => {
    if (typeof window === 'undefined') return '/';
    const p = window.location.pathname;
    if (p === MODULE_BASE_PATH || p.startsWith(`${MODULE_BASE_PATH}/`)) {
      return MODULE_BASE_PATH;
    }
    return '/';
  }, []);

  return (
    <ErrorBoundary title={`${MODULE_TITLE} 运行异常`}>
      <ConfirmProvider>
        <ToastProvider>
          <ModuleUserProvider value={contextValue}>
            {effectiveEmbedded ? (
              <EmbeddedRoutes />
            ) : (
              <BrowserRouter basename={standaloneBasename}>
                <StandaloneShell onLogout={handleStandaloneLogout} />
              </BrowserRouter>
            )}
          </ModuleUserProvider>
        </ToastProvider>
      </ConfirmProvider>
    </ErrorBoundary>
  );
};

export default App;
