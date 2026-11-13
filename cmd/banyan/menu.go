package main

import (
	"fmt"
	"os/exec"

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

	appMenu := menu.AddSubmenu("Banyan")
	appMenu.AddRole(application.About)
	appMenu.AddSeparator()
	appMenu.Add("Settings…").SetAccelerator("CmdOrCtrl+,").OnClick(func(*application.Context) {
		window.Show().Focus()
		window.SetURL("/setup")
	})
	appMenu.AddSeparator()
	appMenu.AddRole(application.ServicesMenu)
	appMenu.AddSeparator()
	appMenu.AddRole(application.Hide)
	appMenu.AddRole(application.HideOthers)
	appMenu.AddRole(application.UnHide)
	appMenu.AddSeparator()
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

	menu.AddRole(application.WindowMenu)

	help := menu.AddSubmenu("Help")
	help.Add("Show Register in Finder").OnClick(func(*application.Context) { reveal(p.register) })
	help.Add("Show Calibration File in Finder").OnClick(func(*application.Context) { reveal(p.calibration) })

	app.Menu.SetApplicationMenu(menu)
}

// reveal selects a file in Finder.
func reveal(path string) { _ = exec.Command("/usr/bin/open", "-R", path).Start() }
