import { Outlet, Link, useLocation } from 'react-router-dom';
import { clsx } from 'clsx';
import { MdDashboard, MdComputer, MdStorage, MdViewInAr, MdHub, MdHealthAndSafety } from 'react-icons/md';

const navigation = [
  { name: 'Dashboard', href: '/dashboard', icon: MdDashboard },
  { name: 'Nodes', href: '/nodes', icon: MdComputer },
  { name: 'Pools', href: '/pools', icon: MdStorage },
  { name: 'Resources', href: '/resources', icon: MdViewInAr },
  { name: 'Gateways', href: '/gateways', icon: MdHub },
  { name: 'HA', href: '/ha', icon: MdHealthAndSafety },
];

export function MainLayout() {
  const location = useLocation();

  return (
    <div className="flex h-screen bg-gray-50">
      {/* Sidebar */}
      <aside className="w-64 bg-gray-900 text-white flex flex-col">
        <div className="p-6 border-b border-gray-800">
          <div className="flex items-center gap-3">
            <MdStorage className="h-8 w-8 text-primary-500" />
            <div>
              <h1 className="text-xl font-bold">SDS Controller</h1>
              <p className="text-sm text-gray-400">Software Defined Storage</p>
            </div>
          </div>
        </div>
        <nav className="flex-1 p-4 space-y-1">
          {navigation.map((item) => {
            const isActive = location.pathname === item.href;
            return (
              <Link
                key={item.name}
                to={item.href}
                className={clsx(
                  'flex items-center gap-3 px-3 py-2 rounded-md text-sm font-medium transition-colors',
                  isActive
                    ? 'bg-primary-600 text-white'
                    : 'text-gray-300 hover:bg-gray-800 hover:text-white'
                )}
              >
                <item.icon className="h-5 w-5" />
                {item.name}
              </Link>
            );
          })}
        </nav>
        <div className="p-4 border-t border-gray-800 text-xs text-gray-500">
          v1.3.0
        </div>
      </aside>

      {/* Main content */}
      <div className="flex-1 flex flex-col overflow-hidden">
        <header className="bg-white border-b border-gray-200 px-6 py-4">
          <h2 className="text-lg font-semibold text-gray-900">
            {navigation.find((n) => n.href === location.pathname)?.name ?? 'Dashboard'}
          </h2>
        </header>
        <main className="flex-1 overflow-auto p-6">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
