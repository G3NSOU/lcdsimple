-- 软路由屏幕状态面板：iStoreOS「服务」里的入口。
-- 只做一件事——把 /etc/config/routerpanel 这张表摊成一个能看状态、能开关的页面，
-- 和 OpenClash/FastNet 那些应用并列。真正的数据都在面板进程里，这里不复制一份。
module("luci.controller.routerpanel", package.seeall)

function index()
	if not nixio.fs.access("/etc/config/routerpanel") then
		return
	end

	local page = entry({ "admin", "services", "routerpanel" }, cbi("routerpanel"), _("Regisseur 面板"), 46)
	page.dependent = true
end
