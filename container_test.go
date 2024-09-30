package container

import "testing"

func TestDockerContainerRun(t *testing.T) {
	dc, err := NewDockerContainer()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		dc.Close()
	}()

	_ = dc.Remove("test")

	id, err := dc.Run("test")
	if err != nil {
		t.Fatal(err)
	}

	t.Log(id)
}

func TestDockerContainerRemove(t *testing.T) {
	dc, err := NewDockerContainer()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		dc.Close()
	}()

	err = dc.Remove("test")
	if err != nil {
		t.Fatal(err)
	}
}

func TestDockerContainerStop(t *testing.T) {
	dc, err := NewDockerContainer()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		dc.Close()
	}()

	err = dc.Stop("test")
	if err != nil {
		t.Fatal(err)
	}
}

func TestDockerContainerStart(t *testing.T) {
	dc, err := NewDockerContainer()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		dc.Close()
	}()
	err = dc.Start("test")
	if err != nil {
		t.Fatal(err)
	}
}
