package main

import (
	"fmt"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// The desk's places, in the order of its own rail. Each is a hash route in the page.
var places = [][2]string{
	{"Desk", "desk"},
	{"Tray", "tray"},
	{"Register", "register"},
	{"Year", "year"},
	{"Monthly", "monthly"},
}

func setupMenu(app *application.App, window *application.WebviewWindow, p paths) {
	menu := app.Menu.New()

	// A Mac's first menu is the app's own; on Windows the same items live
	// under File, and there is no Services, Hide or Window menu.
	appMenu := menu.AddSubmenu(map[bool]string{true: "File", false: "Banyan"}[closeQuits])
	if !closeQuits {
		appMenu.AddRole(application.About)
		appMenu.AddSeparator()
	}
	appMenu.Add("Settings…").SetAccelerator("CmdOrCtrl+,").OnClick(func(*application.Context) {
		window.Show().Focus()
		window.SetURL("/setup")
	})
	// The App Store build is updated by the App Store, the Windows build by its installer.
	if !appStore && hasUpdater {
		appMenu.Add("Check for Updates…").OnClick(func(*application.Context) {
			window.Show().Focus()
			window.SetURL("/setup#updates")
		})
	}
	appMenu.AddSeparator()
	if !closeQuits {
		appMenu.AddRole(application.ServicesMenu)
		appMenu.AddSeparator()
		appMenu.AddRole(application.Hide)
		appMenu.AddRole(application.HideOthers)
		appMenu.AddRole(application.UnHide)
		appMenu.AddSeparator()
	}
	appMenu.AddRole(application.Quit)

	menu.AddRole(application.EditMenu)

	view := menu.AddSubmenu("View")
	for i, place := range places {
		hash := "#/" + place[1]
		view.Add(place[0]).SetAccelerator(fmt.Sprintf("CmdOrCtrl+%d", i+1)).OnClick(func(*application.Context) {
			// From Settings (or any other page) this goes back to the app at that place.
			window.Show()
			window.SetURL("/" + hash)
		})
	}
	view.AddSeparator()
	view.AddRole(application.Reload)
	view.AddRole(application.ResetZoom)
	view.AddRole(application.ZoomIn)
	view.AddRole(application.ZoomOut)
	view.AddRole(application.ToggleFullscreen)

	if !closeQuits {
		menu.AddRole(application.WindowMenu)
	}

	help := menu.AddSubmenu("Help")
	help.Add("Show Register in " + fileManager).OnClick(func(*application.Context) { reveal(p.register) })
	help.Add("Show Calibration File in " + fileManager).OnClick(func(*application.Context) { reveal(p.calibration) })
	if closeQuits {
		help.AddSeparator()
		help.AddRole(application.About)
	}

	app.Menu.SetApplicationMenu(menu)
}
