import type { SubMenuItem, MenuGroup, ModuleMenuConfig } from '@code/common';
import { MODULE_KEY, MODULE_TITLE } from './constants';

export type { SubMenuItem, MenuGroup, ModuleMenuConfig };

// 标准侧边栏菜单契约（W1/W2 全量业务与治理路由）
export const archdocMenuConfig: ModuleMenuConfig = {
  moduleKey: MODULE_KEY,
  moduleName: MODULE_TITLE,
  groups: [
    {
      title: '核心业务',
      items: [
        {
          path: '/repos',
          label: '仓库与文档覆盖',
          icon: 'M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z',
        },
        {
          path: '/drift',
          label: '双维漂移雷达',
          icon: 'M13 10V3L4 14h7v7l9-11h-7z',
        },
        {
          path: '/metrics',
          label: '生成质量度量',
          icon: 'M9 19v-6a2 2 0 00-2-2H5a2 2 0 00-2 2v6a2 2 0 002 2h2a2 2 0 002-2zm0 0V9a2 2 0 012-2h2a2 2 0 012 2v10m-6 0a2 2 0 002 2h2a2 2 0 002-2m0 0V5a2 2 0 012-2h2a2 2 0 012 2v14a2 2 0 01-2 2h-2a2 2 0 01-2-2z',
        },
      ],
    },
    {
      title: '模块治理',
      adminOnly: true, // 仅具备 archdoc_admin 或 super_admin 角色的用户可见
      items: [
        {
          path: '/admin/llm',
          label: 'LLM 模型配置',
          icon: 'M12 6V4m0 2a2 2 0 100 4m0-4a2 2 0 110 4m-6 8a2 2 0 100-4m0 4a2 2 0 110-4m0 4v2m0-6V4m6 6v10m6-2a2 2 0 100-4m0 4a2 2 0 110-4m0 4v2m0-6V4',
          adminOnly: true,
        },
        {
          path: '/admin/doc-types',
          label: '文档类型管理',
          icon: 'M9 12h6m-6 4h6m2 5H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z',
          adminOnly: true,
        },
      ],
    },
  ],
};

export const menuGroups: MenuGroup[] = archdocMenuConfig.groups;
export const menuItems: SubMenuItem[] = archdocMenuConfig.groups.flatMap((group) => group.items);

type MenuChangeListener = (config: ModuleMenuConfig) => void;
const listeners = new Set<MenuChangeListener>();
let currentConfig: ModuleMenuConfig = archdocMenuConfig;

export function subscribeMenuChanges(listener: MenuChangeListener): () => void {
  listeners.add(listener);
  listener(currentConfig);
  return () => {
    listeners.delete(listener);
  };
}

export function notifyMenuUpdated(nextConfig: ModuleMenuConfig): void {
  currentConfig = nextConfig;
  listeners.forEach((fn) => fn(currentConfig));
}

export default archdocMenuConfig;
