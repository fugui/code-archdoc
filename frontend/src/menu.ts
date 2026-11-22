import type { SubMenuItem, MenuGroup, ModuleMenuConfig } from '@code/common';
import { MODULE_KEY, MODULE_TITLE } from './constants';

export type { SubMenuItem, MenuGroup, ModuleMenuConfig };

// 标准侧边栏菜单契约（3 组 7 项：文档全景 / 生产与保鲜 / 模块治理）
export const archdocMenuConfig: ModuleMenuConfig = {
  moduleKey: MODULE_KEY,
  moduleName: MODULE_TITLE,
  groups: [
    {
      title: '文档全景',
      items: [
        {
          path: '/overview',
          label: '全局概览',
          headerTitle: '全局概览 · 文档治理与质量度量大盘',
          icon: 'M3 13h8V3H3v10zm0 8h8v-6H3v6zm10 0h8V11h-8v10zm0-18v6h8V3h-8z',
        },
        {
          path: '/repos',
          label: '仓库文档覆盖',
          icon: 'M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z',
        },
        {
          path: '/arch-tree',
          label: '架构文档全景',
          icon: 'M4 5a1 1 0 011-1h4a1 1 0 011 1v4a1 1 0 01-1 1H5a1 1 0 01-1-1V5zM14 5a1 1 0 011-1h4a1 1 0 011 1v4a1 1 0 01-1 1h-4a1 1 0 01-1-1V5zM14 15a1 1 0 011-1h4a1 1 0 011 1v4a1 1 0 01-1 1h-4a1 1 0 01-1-1v-4zM7 10v7h7M7 7h7',
        },
      ],
    },
    {
      title: '生产与保鲜',
      items: [
        {
          path: '/tasks',
          label: '反推任务中心',
          icon: 'M9 5H7a2 2 0 00-2 2v12a2 2 0 002 2h10a2 2 0 002-2V7a2 2 0 00-2-2h-2M9 5a2 2 0 002 2h2a2 2 0 002-2M9 5a2 2 0 012-2h2a2 2 0 012 2m-6 9l2 2 4-4',
        },
        {
          path: '/drift',
          label: '双维漂移雷达',
          icon: 'M13 10V3L4 14h7v7l9-11h-7z',
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
        {
          path: '/admin/ops',
          label: '运行与存储运维',
          icon: 'M5 12h14M5 12a2 2 0 01-2-2V6a2 2 0 012-2h14a2 2 0 012 2v4a2 2 0 01-2 2M5 12a2 2 0 00-2 2v4a2 2 0 002 2h14a2 2 0 002-2v-4a2 2 0 00-2-2m-2-4h.01M17 16h.01',
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
